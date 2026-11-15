package memo

import (
	"errors"
	"math/rand/v2"
	"sync"
	"time"
)

// Memo holds one derived value per user for upstream facts that barely change.
type Memo[T any] struct {
	ttl        time.Duration
	background bool

	mu      sync.Mutex
	entries map[string]*memoEntry[T]
}

type memoEntry[T any] struct {
	mu         sync.Mutex
	val        T
	fetched    time.Time
	loading    chan struct{}
	version    uint64
	retryAfter time.Time
	// failures counts the loads that failed in a row, delay the last one put the entry off by,
	// and err the last failure, answered again while the entry is off and has nothing to serve.
	failures int
	delay    time.Duration
	err      error
}

func (m *Memo[T]) entry(user string) *memoEntry[T] {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[user]
	if !ok {
		e = &memoEntry[T]{}
		m.entries[user] = e
	}
	return e
}

func (m *Memo[T]) held(user string) *memoEntry[T] {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[user]
}

// The caller holds e.mu.
func (e *memoEntry[T]) settle(version uint64, val T, err error) {
	if err == nil && version == e.version {
		e.val, e.fetched, e.err = val, time.Now(), nil
		// A load that answered ends the backoff its forerunner earned.
		e.failures, e.delay, e.retryAfter = 0, 0, time.Time{}
	} else if err != nil {
		e.err = err
	}
	close(e.loading)
	e.loading = nil
}

// New returns a memo that reloads synchronously when stale.
func New[T any](ttl time.Duration) *Memo[T] {
	return &Memo[T]{ttl: ttl, entries: make(map[string]*memoEntry[T])}
}

// NewBackground returns a memo that serves stale values while reloading in the background; load must not depend on its caller's lifetime.
func NewBackground[T any](ttl time.Duration) *Memo[T] {
	return &Memo[T]{ttl: ttl, background: true, entries: make(map[string]*memoEntry[T])}
}

// Get returns the user's value, loading it once for concurrent callers when none is cached.
func (m *Memo[T]) Get(user string, load func() (T, error)) (T, error) {
	if m.ttl == 0 {
		return load()
	}
	e := m.entry(user)

	for {
		e.mu.Lock()
		if !e.fetched.IsZero() && (m.background || time.Since(e.fetched) <= m.ttl) {
			if m.background && time.Since(e.fetched) > m.ttl {
				m.start(e, load)
			}
			val := e.val
			e.mu.Unlock()
			return val, nil
		}
		if e.loading != nil {
			done := e.loading
			e.mu.Unlock()
			<-done
			continue
		}
		// Off after a failure and nothing to serve, the stored failure is answered until its delay is up.
		if e.fetched.IsZero() && e.err != nil && time.Now().Before(e.retryAfter) {
			err := e.err
			e.mu.Unlock()
			var zero T
			return zero, err
		}
		e.loading = make(chan struct{})
		version := e.version
		e.mu.Unlock()
		val, err := load()
		m.loaded(e, version, val, err)
		// A first load overtaken by Stale returns what it read without keeping it.
		e.mu.Lock()
		if err == nil && !e.fetched.IsZero() {
			val = e.val
		}
		e.mu.Unlock()
		return val, err
	}
}

// Failed background reads back off so a busy page cannot hammer an outage: the delay doubles per
// failure to the cap, with jitter, unless the error says when to ask again, as a server answering
// 429 does with its Retry-After (RFC 9110 10.2.3).
const (
	memoRetryDelay = 30 * time.Second
	memoRetryCap   = 5 * time.Minute
)

// Retrier is an error that says when the read it failed may be asked again. A load's error that
// names one puts the memo off until then instead of the default backoff.
type Retrier interface {
	RetryAfter() time.Time
}

// The caller holds e.mu: the delay the entry is currently put off by, the default before any failure.
func (e *memoEntry[T]) retryDelay() time.Duration {
	if e.delay == 0 {
		return memoRetryDelay
	}
	return e.delay
}

// backoff is how long the failed load puts the entry off, and records it against the next failure.
// The caller holds e.mu.
func backoff[T any](e *memoEntry[T], err error) time.Duration {
	var r Retrier
	if errors.As(err, &r) {
		if d := time.Until(r.RetryAfter()); d > 0 {
			// The server said when; its word ends the doubling.
			e.failures, e.delay = 0, d
			return d
		}
	}
	e.failures++
	d := min(memoRetryDelay<<uint(min(e.failures-1, 4)), memoRetryCap)
	d = d * time.Duration(80+rand.IntN(41)) / 100 // a fifth either way, so retries do not sync up
	e.delay = d
	return d
}

func (m *Memo[T]) start(e *memoEntry[T], load func() (T, error)) {
	if e.loading != nil || time.Now().Before(e.retryAfter) {
		return
	}
	e.loading = make(chan struct{})
	version := e.version
	go func() {
		val, err := load()
		m.loaded(e, version, val, err)
	}()
}

// loaded records a finished load, earning the entry its backoff when it failed.
func (m *Memo[T]) loaded(e *memoEntry[T], version uint64, val T, err error) {
	e.mu.Lock()
	if err != nil {
		e.retryAfter = time.Now().Add(backoff(e, err))
	}
	e.settle(version, val, err)
	e.mu.Unlock()
}

// Warm returns what is cached for the user without waiting, loading in the background when missing or stale.
func (m *Memo[T]) Warm(user string, load func() (T, error)) T {
	if m.ttl == 0 {
		var zero T
		return zero
	}
	e := m.entry(user)

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.fetched.IsZero() || time.Since(e.fetched) > m.ttl {
		m.start(e, load)
	}
	return e.val
}

// Put sets the user's value from a load the caller already made.
func (m *Memo[T]) Put(user string, val T) {
	if m.ttl == 0 {
		return
	}
	e := m.entry(user)
	e.mu.Lock()
	e.version++
	e.val, e.fetched = val, time.Now()
	e.mu.Unlock()
}

// Update rewrites the user's value in place when one is held.
func (m *Memo[T]) Update(user string, f func(T) T) {
	e := m.held(user)
	if e == nil {
		return
	}
	e.mu.Lock()
	if !e.fetched.IsZero() {
		e.version++
		e.val = f(e.val)
	}
	e.mu.Unlock()
}

// Forget drops the user's value.
func (m *Memo[T]) Forget(user string) {
	m.mu.Lock()
	delete(m.entries, user)
	m.mu.Unlock()
}

// Retry keeps the user's value but lets it go stale after the retry delay; until, when not zero,
// is a server's own ask to be asked no sooner, which holds even against the doubled backoff.
func (m *Memo[T]) Retry(user string, until time.Time) {
	e := m.held(user)
	if e == nil {
		return
	}
	e.mu.Lock()
	if until.After(e.retryAfter) {
		e.retryAfter = until
	}
	if soon := time.Now().Add(e.retryDelay() - m.ttl); e.fetched.After(soon) {
		e.fetched = soon
	}
	e.mu.Unlock()
}

// Stale retains the last value while scheduling its next background refresh.
func (m *Memo[T]) Stale(user string) {
	e := m.held(user)
	if e == nil {
		return
	}
	e.mu.Lock()
	e.version++
	if !e.fetched.IsZero() {
		e.fetched = time.Now().Add(-m.ttl - time.Nanosecond)
	}
	e.mu.Unlock()
}
