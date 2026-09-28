package memo

import (
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
		e.val, e.fetched = val, time.Now()
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
		e.loading = make(chan struct{})
		version := e.version
		e.mu.Unlock()
		val, err := load()
		e.mu.Lock()
		e.settle(version, val, err)
		// A first load overtaken by Stale returns what it read without keeping it.
		if err == nil && !e.fetched.IsZero() {
			val = e.val
		}
		e.mu.Unlock()
		return val, err
	}
}

// Failed background reads back off so a busy page cannot hammer an outage.
const memoRetryDelay = 5 * time.Second

func (m *Memo[T]) start(e *memoEntry[T], load func() (T, error)) {
	if e.loading != nil || time.Now().Before(e.retryAfter) {
		return
	}
	e.loading = make(chan struct{})
	version := e.version
	go func() {
		val, err := load()
		e.mu.Lock()
		if err != nil {
			e.retryAfter = time.Now().Add(memoRetryDelay)
		}
		e.settle(version, val, err)
		e.mu.Unlock()
	}()
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

// Retry keeps the user's value but lets it go stale after the retry delay.
func (m *Memo[T]) Retry(user string) {
	e := m.held(user)
	if e == nil {
		return
	}
	e.mu.Lock()
	if soon := time.Now().Add(memoRetryDelay - m.ttl); e.fetched.After(soon) {
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
