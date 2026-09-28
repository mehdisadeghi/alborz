package alborz

import (
	"sync"
	"time"
)

// Memo holds one derived value per user, for upstream facts that cost
// round trips yet barely change. Values live as long as the process; the
// ttl decides when one is stale. A memo with no ttl holds nothing.
type Memo[T any] struct {
	ttl time.Duration
	// background serves a stale value while one reload runs behind.
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

// entry is the user's, made when there is none.
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

// settle ends a load begun at version, keeping what it read unless the
// value was set meanwhile; the caller holds e.mu.
func (e *memoEntry[T]) settle(version uint64, val T, err error) {
	if err == nil && version == e.version {
		e.val, e.fetched = val, time.Now()
	}
	close(e.loading)
	e.loading = nil
}

// NewMemo returns a memo that reloads synchronously when stale.
func NewMemo[T any](ttl time.Duration) *Memo[T] {
	return &Memo[T]{ttl: ttl, entries: make(map[string]*memoEntry[T])}
}

// NewBackgroundMemo returns a memo that serves stale values immediately and
// reloads them in the background, one reload at a time. load must not
// depend on its caller's lifetime.
func NewBackgroundMemo[T any](ttl time.Duration) *Memo[T] {
	return &Memo[T]{ttl: ttl, background: true, entries: make(map[string]*memoEntry[T])}
}

// Get returns the user's value, calling load when none is cached yet.
// Concurrent callers for one user wait for a single load instead of each
// running their own. Failures are not remembered, so the next caller
// retries; a failed background reload leaves the old value standing.
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
		// A first load overtaken by Stale has nothing held to give way
		// to: what it read is answered, and not kept.
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

// Warm returns whatever is cached for the user and starts a background
// load when nothing is or the value has gone stale. It never waits: a
// page that only wants the value as a convenience gets what is ready,
// which on a first visit is the zero value. load must not depend on its
// caller's lifetime.
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

// Put sets the user's value from a load the caller already made, so a
// fact fetched for another reason is not fetched again.
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

// Update rewrites the user's value in place, for a change the caller
// already knows the effect of; nothing to do when none is held.
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

// Forget drops the user's value, for writes that invalidate it.
func (m *Memo[T]) Forget(user string) {
	m.mu.Lock()
	delete(m.entries, user)
	m.mu.Unlock()
}

// Retry keeps the user's value but lets it go stale after the retry
// delay, for a value only partly there - a listing a server was missing
// from - so the next reader after that asks the missing part again.
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
