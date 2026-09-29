package alborz

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// loginRetryAfter keeps one unreachable server from turning every request
	// into a burst of logins that a provider may count against the account.
	loginRetryAfter = 2 * time.Minute
)

// signInTries within signInWindow pause the sign-in form for one reader:
// fail2ban's defaults for maxretry and findtime.
const (
	signInTries  = 5
	signInWindow = 10 * time.Minute
)

// PausedError is a sign-in withheld until Until after signInTries refusals.
type PausedError struct{ Until time.Time }

func (e PausedError) Error() string {
	return fmt.Sprintf("too many refused passwords; paused until %v", e.Until.Format(time.RFC3339))
}

// SignIn opens a login for username, counting refusals per reader at from.
func (s *Server) SignIn(username, password, from string) (*Login, error) {
	var login *Login
	err := s.signInRefused.attempt(readerKey(from, username), func() (err error) {
		login, err = s.logins.put(username, password, from)
		return err
	})
	return login, err
}

func readerKey(from, username string) string {
	if RequestAddress(from) == "" {
		return from + "\x00" + strings.ToLower(username)
	}
	return from
}

// CheckPassword asks the mail server whether password opens username, as signing in does and sharing its tries, keeping no login.
func (s *Server) CheckPassword(username, password, from string) error {
	return s.signInRefused.attempt(readerKey(from, username), func() error {
		_, domain, _ := strings.Cut(username, "@")
		c, err := s.logins.connectIMAP(domain, username, password, from)
		if err != nil {
			return err
		}
		c.Logout()
		return nil
	})
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

// Restore signs a remembered account back in. A failure holds the account back
// for loginRetryAfter, during which it answers nil and no error.
func (s *Server) Restore(username, password, from string) (*Login, error) {
	if s.recentlyFailed(username) {
		return nil, nil
	}
	login, err := s.logins.put(username, password, from)
	if err != nil {
		s.loginFailed(username)
		return nil, err
	}
	s.loginFailures.Delete(username)
	return login, nil
}

func (s *Server) recentlyFailed(username string) bool {
	when, ok := s.loginFailures.Load(username)
	if !ok {
		return false
	}
	if time.Since(when.(time.Time)) < loginRetryAfter {
		return true
	}
	s.loginFailures.CompareAndDelete(username, when)
	return false
}

// loginFailed also drops expired failures, so the map does not grow for good.
func (s *Server) loginFailed(username string) {
	now := time.Now()
	s.loginFailures.Range(func(k, when any) bool {
		if now.Sub(when.(time.Time)) >= loginRetryAfter {
			s.loginFailures.CompareAndDelete(k, when)
		}
		return true
	})
	s.loginFailures.Store(username, now)
}

// RequestAddress is "" for loopback and private addresses: an untrusted proxy
// in front of alborz, which says nothing about the reader.
func RequestAddress(from string) string {
	ip, err := netip.ParseAddr(from)
	if err != nil || !publicAddr(ip) {
		return ""
	}
	return from
}
