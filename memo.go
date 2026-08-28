package alborz

import (
	"sync"
	"time"
)

// Memo holds one derived value per user. It exists for upstream facts that
// cost round trips to answer yet barely change, like which calendars an
// account has or which folder carries a special-use role. Values are kept
// as long as the process lives; the ttl only decides when one counts as
// stale.
type Memo[T any] struct {
	ttl time.Duration
	// A background memo serves a stale value as it is while one reload
	// runs behind the scenes, so a returning user never waits on facts
	// that almost never change.
	background bool

	mu      sync.Mutex
	entries map[string]*memoEntry[T]
}

type memoEntry[T any] struct {
	mu        sync.Mutex
	val       T
	fetched   time.Time
	reloading bool
}

// NewMemo returns a memo that reloads synchronously when stale.
func NewMemo[T any](ttl time.Duration) *Memo[T] {
	return &Memo[T]{ttl: ttl, entries: make(map[string]*memoEntry[T])}
}

// NewBackgroundMemo returns a memo that serves stale values immediately and
// reloads them in the background, one reload at a time. load must not
// depend on its caller's lifetime.
func NewBackgroundMemo[T any](ttl time.Duration) *Memo[T] {
	m := NewMemo[T](ttl)
	m.background = true
	return m
}

// Get returns the user's value, calling load when none is cached yet.
// Concurrent callers for one user wait for a single load instead of each
// running their own. Failures are not remembered, so the next caller
// retries; a failed background reload leaves the old value standing.
func (m *Memo[T]) Get(user string, load func() (T, error)) (T, error) {
	m.mu.Lock()
	e, ok := m.entries[user]
	if !ok {
		e = &memoEntry[T]{}
		m.entries[user] = e
	}
	m.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.fetched.IsZero() || (!m.background && time.Since(e.fetched) > m.ttl) {
		val, err := load()
		if err != nil {
			var zero T
			return zero, err
		}
		e.val, e.fetched = val, time.Now()
		return val, nil
	}

	if m.background && time.Since(e.fetched) > m.ttl && !e.reloading {
		e.reloading = true
		go func() {
			val, err := load()
			e.mu.Lock()
			e.reloading = false
			if err == nil {
				e.val, e.fetched = val, time.Now()
			}
			e.mu.Unlock()
		}()
	}
	return e.val, nil
}

// Forget drops the user's value, for writes that invalidate it.
func (m *Memo[T]) Forget(user string) {
	m.mu.Lock()
	delete(m.entries, user)
	m.mu.Unlock()
}
