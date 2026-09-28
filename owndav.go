package alborz

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz/plugins/collections"
	"github.com/labstack/echo/v4"
)

// Each check is an IMAP login, and a syncing phone sends its password with every request.
const davPasswordLife = 5 * time.Minute

// davRealm is what a DAV client shows beside its password prompt.
const davRealm = AppName

// wellKnown are where a client given only the host starts (RFC 6764).
var wellKnown = []string{"/.well-known/caldav", "/.well-known/carddav"}

// Refusals are cached too, so a phone's stale password does not spend the reader's sign-in tries.
type davPasswords struct {
	mu      sync.Mutex
	salt    [32]byte
	checked map[[sha256.Size]byte]davVerdict
}

type davVerdict struct {
	refused error
	at      time.Time
}

func newDAVPasswords() *davPasswords {
	p := &davPasswords{checked: make(map[[sha256.Size]byte]davVerdict)}
	if _, err := rand.Read(p.salt[:]); err != nil {
		panic(err)
	}
	return p
}

func (p *davPasswords) digest(username, password string) [sha256.Size]byte {
	return sha256.Sum256(slices.Concat(p.salt[:], []byte(username), []byte{0}, []byte(password)))
}

func (p *davPasswords) known(username, password string, now time.Time) (davVerdict, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	was, ok := p.checked[p.digest(username, password)]
	return was, ok && now.Sub(was.at) < davPasswordLife
}

func (p *davPasswords) keep(username, password string, refused error, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	maps.DeleteFunc(p.checked, func(_ [sha256.Size]byte, v davVerdict) bool {
		return now.Sub(v.at) >= davPasswordLife
	})
	p.checked[p.digest(username, password)] = davVerdict{refused, now}
}

// refusalsHeld bounds the keys remembered, so a flood of addresses cannot grow the map.
const refusalsHeld = 4096

// refusals stops asking the mail server after tries refusals within window, since each ask is an IMAP login.
type refusals struct {
	tries  int
	window time.Duration

	mu sync.Mutex
	at map[string][]time.Time
	// A full map is not walked again before something can be dropped.
	sweepAt time.Time
}

func newRefusals(tries int, window time.Duration) *refusals {
	return &refusals{tries: tries, window: window, at: make(map[string][]time.Time)}
}

func (r *refusals) recent(key string, now time.Time) []time.Time {
	var out []time.Time
	for _, at := range r.at[key] {
		if now.Sub(at) < r.window {
			out = append(out, at)
		}
	}
	return out
}

func (r *refusals) sweep(now time.Time) {
	r.sweepAt = time.Time{}
	for k := range r.at {
		recent := r.recent(k, now)
		if len(recent) == 0 {
			delete(r.at, k)
			continue
		}
		if ends := recent[0].Add(r.window); r.sweepAt.IsZero() || ends.Before(r.sweepAt) {
			r.sweepAt = ends
		}
	}
}

// Reserving before the try keeps concurrent tries from all passing the count.
func (r *refusals) reserve(key string, now time.Time) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	recent := r.recent(key, now)
	if len(recent) >= r.tries {
		return recent[len(recent)-r.tries].Add(r.window)
	}
	if _, held := r.at[key]; !held && len(r.at) >= refusalsHeld {
		if now.Before(r.sweepAt) {
			return r.sweepAt
		}
		if r.sweep(now); len(r.at) >= refusalsHeld {
			return r.sweepAt
		}
	}
	r.at[key] = append(recent, now)
	return time.Time{}
}

func (r *refusals) release(key string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Two concurrent tries may share a clock tick, so only one is released.
	i := slices.IndexFunc(r.at[key], func(at time.Time) bool { return at.Equal(now) })
	if i < 0 {
		return
	}
	kept := slices.Delete(r.at[key], i, i+1)
	if len(kept) == 0 {
		delete(r.at, key)
		return
	}
	r.at[key] = kept
}

func (r *refusals) attempt(key string, ask func() error) error {
	now := time.Now()
	if until := r.reserve(key, now); !until.IsZero() {
		return PausedError{Until: until}
	}
	err := ask()
	if !errors.As(err, new(AuthError)) {
		r.release(key, now)
	}
	return err
}

// authenticateDAV checks a DAV client's password the way signing in does, sharing its tries.
func (s *Server) authenticateDAV(username, password, source string) error {
	username = strings.ToLower(username)
	now := time.Now()
	if was, ok := s.davPasswords.known(username, password, now); ok {
		return was.refused
	}
	err := s.signInRefused.attempt(readerKey(source, username), func() error {
		_, domain, _ := strings.Cut(username, "@")
		c, err := s.Logins.connectIMAP(domain, username, password, source)
		if err != nil {
			return err
		}
		c.Logout()
		return nil
	})
	if err == nil || errors.As(err, new(AuthError)) {
		s.davPasswords.keep(username, password, err, now)
	}
	return err
}

// serveOwnDAV answers DAV clients ahead of sessions and cookies, by address and password on each request.
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
			// A published calendar needs no credentials.
			if collections.IsPublic(r.URL.Path) {
				s.Collections.ServePublic(w, r)
				return nil
			}
			if !strings.HasPrefix(r.URL.Path, collections.Prefix+"/") {
				return next(ectx)
			}
			username, password, ok := r.BasicAuth()
			if ok {
				err := s.authenticateDAV(username, password, ectx.RealIP())
				var upstream UpstreamError
				if errors.As(err, &upstream) {
					http.Error(w, err.Error(), http.StatusServiceUnavailable)
					return nil
				}
				// Not 401 for a paused reader: a client hearing 401 asks its owner for a new password.
				var paused PausedError
				if errors.As(err, &paused) {
					w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(time.Until(paused.Until).Seconds()))))
					http.Error(w, err.Error(), http.StatusTooManyRequests)
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
