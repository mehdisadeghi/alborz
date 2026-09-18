package alborz

import (
	"context"
	"errors"
	"sync"
)

// A burst of tabs must not leave an unbounded queue behind a slow server.
const imapQueueLimit = 64

var errIMAPQueueFull = errors.New("mail request queue is full")

type imapWaiter struct {
	ready      chan struct{}
	background bool
}

// imapQueue gives interactive work priority between background batches.
// Each connection still has exactly one owner, including its SELECT state.
type imapQueue struct {
	mu      sync.Mutex
	busy    bool
	waiters []*imapWaiter
}

func (q *imapQueue) acquire(ctx context.Context, done <-chan struct{}, background bool) error {
	q.mu.Lock()
	if err := ctx.Err(); err != nil {
		q.mu.Unlock()
		return err
	}
	select {
	case <-done:
		q.mu.Unlock()
		return context.Canceled
	default:
	}
	if !q.busy {
		q.busy = true
		q.mu.Unlock()
		return nil
	}
	if done != nil && len(q.waiters) >= imapQueueLimit {
		q.mu.Unlock()
		return errIMAPQueueFull
	}
	w := &imapWaiter{ready: make(chan struct{}), background: background}
	q.waiters = append(q.waiters, w)
	q.mu.Unlock()
	var err error
	select {
	case <-w.ready:
		return nil
	case <-ctx.Done():
		err = ctx.Err()
	case <-done:
		err = context.Canceled
	}
	q.mu.Lock()
	for i, waiting := range q.waiters {
		if waiting == w {
			q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
			q.mu.Unlock()
			return err
		}
	}
	// Ownership may have arrived at the same instant as cancellation.
	q.mu.Unlock()
	q.release()
	return err
}

func (q *imapQueue) release() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.waiters) == 0 {
		q.busy = false
		return
	}
	next := 0
	for i, w := range q.waiters {
		if !w.background {
			next = i
			break
		}
	}
	w := q.waiters[next]
	q.waiters = append(q.waiters[:next], q.waiters[next+1:]...)
	close(w.ready)
}

// WithIMAPStart lets shared speculative work be withdrawn until it holds
// the connection: start then says whether it may still run.
func WithIMAPStart(ctx context.Context, start func() bool) context.Context {
	return context.WithValue(ctx, imapStartKey{}, start)
}

type imapStartKey struct{}
