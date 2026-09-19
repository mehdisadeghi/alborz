package alborz

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz/plugins/collections"
	"github.com/labstack/echo/v4"
)

// davPasswordLife is how long a password the mail server accepted is
// taken again without asking it: a phone syncing sends one with every
// request, and each check is an IMAP login.
const davPasswordLife = 5 * time.Minute

// davRefusalPause is how long a refused address and source are refused
// again without asking the mail server. Each check is an IMAP login, so
// a client guessing passwords would make alborz guess them at the mail
// server, and it is alborz's address the server's own lockout bans. A
// phone with a stale password retries every few seconds; a pause this
// long keeps that to a trickle and delays a corrected one by as much.
const davRefusalPause = 30 * time.Second

// davRefusalsHeld bounds the refusals remembered: past it the expired
// ones are dropped, so a flood of addresses cannot grow the map for ever.
const davRefusalsHeld = 4096

// davRealm is what a DAV client shows beside its password prompt.
const davRealm = AppName

// wellKnown are where a client given only the host starts (RFC 6764 5).
var wellKnown = []string{"/.well-known/caldav", "/.well-known/carddav"}

// davPasswords remembers, per account, the password last accepted, as
// a salted digest: nothing in it signs anybody in anywhere.
type davPasswords struct {
	mu      sync.Mutex
	salt    [32]byte
	checked map[string]davPassword
	// refused holds when an address was last refused from a source,
	// keyed by both: one source guessing must not keep the owner's own
	// phone out.
	refused map[string]time.Time
}

// errDAVPaused is a check not made, because the same address from the
// same source was refused a moment ago.
var errDAVPaused = errors.New("refused a moment ago; not asking again yet")

type davPassword struct {
	digest [sha256.Size]byte
	at     time.Time
}

func newDAVPasswords() *davPasswords {
	p := &davPasswords{checked: make(map[string]davPassword), refused: make(map[string]time.Time)}
	if _, err := rand.Read(p.salt[:]); err != nil {
		panic(err)
	}
	return p
}

func (p *davPasswords) digest(password string) [sha256.Size]byte {
	return sha256.Sum256(append(p.salt[:], password...))
}

func (p *davPasswords) known(username, password string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	was, ok := p.checked[username]
	digest := p.digest(password)
	return ok && now.Sub(was.at) < davPasswordLife && subtle.ConstantTimeCompare(was.digest[:], digest[:]) == 1
}

func (p *davPasswords) keep(username, password string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checked[username] = davPassword{p.digest(password), now}
}

func (p *davPasswords) paused(key string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	at, ok := p.refused[key]
	return ok && now.Sub(at) < davRefusalPause
}

func (p *davPasswords) refuse(key string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.refused) >= davRefusalsHeld {
		for k, at := range p.refused {
			if now.Sub(at) >= davRefusalPause {
				delete(p.refused, k)
			}
		}
	}
	p.refused[key] = now
}

// authenticateDAV checks a DAV client's address and password the way
// signing in does: the mail server of a served domain says.
func (s *Server) authenticateDAV(username, password, source string) error {
	username = strings.ToLower(username)
	now := time.Now()
	if s.davPasswords.known(username, password, now) {
		return nil
	}
	key := username + "\x00" + source
	if s.davPasswords.paused(key, now) {
		return errDAVPaused
	}
	_, domain, _ := strings.Cut(username, "@")
	c, err := s.Sessions.connectIMAP(domain, username, password)
	var refused AuthError
	if errors.As(err, &refused) {
		s.davPasswords.refuse(key, now)
	}
	if err != nil {
		return err
	}
	c.Logout()
	s.davPasswords.keep(username, password, now)
	return nil
}

// serveOwnDAV answers the calendars and address books kept here to a
// DAV client, ahead of everything a page goes through: no visit, no
// cookie, no login page, only the account's address and password on
// each request.
func (s *Server) serveOwnDAV() echo.MiddlewareFunc {
	dav := s.Collections.Handler()
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ectx echo.Context) error {
			w, r := ectx.Response(), ectx.Request()
			for _, start := range wellKnown {
				if r.URL.Path == start {
					http.Redirect(w, r, collections.Prefix+"/", http.StatusPermanentRedirect)
					return nil
				}
			}
			// A published calendar has an address of its own, outside the
			// DAV server's, which a subscribing client needs no more of.
			if collections.IsPublic(r.URL.Path) {
				s.Collections.ServePublic(w, r)
				return nil
			}
			if !strings.HasPrefix(r.URL.Path, collections.Prefix+"/") {
				return next(ectx)
			}
			username, password, ok := r.BasicAuth()
			if ok {
				// The connection's own address: a forwarded-for header is
				// the client's to write, and would reset the pause at will.
				source, _, _ := net.SplitHostPort(r.RemoteAddr)
				err := s.authenticateDAV(username, password, source)
				var upstream UpstreamError
				if errors.As(err, &upstream) {
					http.Error(w, err.Error(), http.StatusServiceUnavailable)
					return nil
				}
				ok = err == nil
			}
			if !ok {
				w.Header().Set("WWW-Authenticate", `Basic realm="`+davRealm+`", charset="UTF-8"`)
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return nil
			}
			dav.ServeHTTP(w, r.WithContext(collections.As(r.Context(), username)))
			return nil
		}
	}
}
