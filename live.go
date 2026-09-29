package alborz

import (
	"sync"
)

// Change says a folder of an account changed; it stays unrendered for the reader's language.
type Change struct {
	Account string
	Mailbox string
}

// Changes fans changes out to open streams without waiting on slow readers.
type Changes struct {
	mu     sync.Mutex
	subs   map[chan Change]struct{}
	closed bool
}

func newChanges() *Changes {
	return &Changes{subs: map[chan Change]struct{}{}}
}

// Listen returns a channel of changes and the function that closes it.
func (c *Changes) Listen() (<-chan Change, func()) {
	ch := make(chan Change, 8)
	c.mu.Lock()
	if c.closed {
		close(ch)
	} else {
		c.subs[ch] = struct{}{}
	}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		if _, ok := c.subs[ch]; ok {
			delete(c.subs, ch)
			close(ch)
		}
		c.mu.Unlock()
	}
}

// Publish tells every listener, dropping the change for one whose buffer is full.
func (c *Changes) Publish(ch Change) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for sub := range c.subs {
		select {
		case sub <- ch:
		default:
		}
	}
}

// Close ends every stream, and every one opened after, so shutdown does not wait on open tabs.
func (c *Changes) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for sub := range c.subs {
		delete(c.subs, sub)
		close(sub)
	}
}

// Announce publishes that a folder of an account changed.
func (c *Changes) Announce(account, mailbox string) {
	c.Publish(Change{Account: account, Mailbox: mailbox})
}
