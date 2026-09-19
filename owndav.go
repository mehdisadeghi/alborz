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

// davPasswordLife is how long a password the mail server accepted is
// taken again without asking it: a phone syncing sends one with every
// request, and each check is an IMAP login.
const davPasswordLife = 5 * time.Minute

// davRealm is what a DAV client shows beside its password prompt.
const davRealm = AppName

// wellKnown are where a client given only the host starts (RFC 6764 5).
var wellKnown = []string{"/.well-known/caldav", "/.well-known/carddav"}

// davPasswords remembers what the mail server last said of an address
// and a password, under their salted digest: nothing in it signs anybody
// in anywhere. A refusal is remembered as an acceptance is, because a
// phone with a stale password sends it every few seconds, and asking
// again each time would spend the reader's sign-in tries on one answer.
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

// known is the verdict still held for this address and password.
func (p *davPasswords) known(username, password string, now time.Time) (davVerdict, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	was, ok := p.checked[p.digest(username, password)]
	return was, ok && now.Sub(was.at) < davPasswordLife
}

// keep holds a verdict, and drops the ones past their life first. An
// account has one password accepted and every refusal cost a counted
// try, so the tries allowed bound what is held.
func (p *davPasswords) keep(username, password string, refused error, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	maps.DeleteFunc(p.checked, func(_ [sha256.Size]byte, v davVerdict) bool {
		return now.Sub(v.at) >= davPasswordLife
	})
	p.checked[p.digest(username, password)] = davVerdict{refused, now}
}

// refusalsHeld bounds the keys remembered: past it the expired ones are
// dropped, and while none has expired a key not yet held is paused, so
// a flood of addresses cannot grow the map for ever.
const refusalsHeld = 4096

// refusals remembers when a reader was refused a password, so that past
// tries refusals within window alborz stops asking the mail server. Each
// ask is an IMAP login, and a server's own lockout counts alborz's.
type refusals struct {
	tries  int
	window time.Duration

	mu sync.Mutex
	at map[string][]time.Time
	// sweepAt is when the oldest refusal held runs out: a full map is
	// not walked again before there is something to drop.
	sweepAt time.Time
}

func newRefusals(tries int, window time.Duration) *refusals {
	return &refusals{tries: tries, window: window, at: make(map[string][]time.Time)}
}

// recent are key's refusals inside the window, oldest first.
func (r *refusals) recent(key string, now time.Time) []time.Time {
	var out []time.Time
	for _, at := range r.at[key] {
		if now.Sub(at) < r.window {
			out = append(out, at)
		}
	}
	return out
}

// sweep drops the keys whose refusals have all run out.
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

// reserve counts a try for key before it is made, and answers when key
// may try again if it may not now. Tries made side by side would
// otherwise all pass the count before any of them is refused.
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

// release takes back the try reserved at now, which was not refused.
func (r *refusals) release(key string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// One try, not every try of that instant: two side by side may
	// have been reserved on the same tick of the clock.
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

// attempt asks the mail server on key's behalf, unless key has had its
// refusals; only an answer that is a refusal stays counted.
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

// authenticateDAV checks a DAV client's address and password the way
// signing in does: the mail server of a served domain says, and the
// tries are the same reader's as at the sign-in form.
func (s *Server) authenticateDAV(username, password, source string) error {
	username = strings.ToLower(username)
	now := time.Now()
	if was, ok := s.davPasswords.known(username, password, now); ok {
		return was.refused
	}
	err := s.signInRefused.attempt(readerKey(source, username), func() error {
		_, domain, _ := strings.Cut(username, "@")
		c, err := s.Sessions.connectIMAP(domain, username, password, source)
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
				err := s.authenticateDAV(username, password, ectx.RealIP())
				var upstream UpstreamError
				if errors.As(err, &upstream) {
					http.Error(w, err.Error(), http.StatusServiceUnavailable)
					return nil
				}
				// A paused reader is not told the password is wrong: a
				// client that hears 401 asks its owner for a new one.
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
