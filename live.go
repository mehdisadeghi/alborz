package alborz

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
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

// livePing keeps proxies from dropping an idle stream.
const livePing = 25 * time.Second

func handleEvents(ctx *Context) error {
	s := ctx.Session()
	// The stream outlives its request, so each change rechecks the session's accounts.
	mine := func(account string) bool {
		live, _ := s.Accounts()
		return slices.ContainsFunc(live, func(s *Login) bool { return s.Username() == account })
	}
	changes, stop := ctx.Server.Changes.Listen()
	defer stop()

	res := ctx.Response()
	res.Header().Set("Content-Type", "text/event-stream")
	res.Header().Set("Cache-Control", "no-cache")
	res.Header().Set("X-Accel-Buffering", "no")
	res.WriteHeader(http.StatusOK)
	res.Flush()

	ping := time.NewTicker(livePing)
	defer ping.Stop()
	for {
		if s.Locked(time.Now()) {
			fmt.Fprint(res, "event: locked\ndata:\n\n")
			res.Flush()
			return nil
		}
		select {
		case <-ctx.Request().Context().Done():
			return nil
		case <-ping.C:
			if _, err := fmt.Fprint(res, ": ping\n\n"); err != nil {
				return nil
			}
			res.Flush()
		case change, ok := <-changes:
			if !ok {
				return nil
			}
			if !mine(change.Account) {
				continue
			}
			// A newline in either value would end the frame.
			_, err := fmt.Fprintf(res, "event: mailbox\ndata: %s %s\n\n",
				oneLine(change.Account), oneLine(change.Mailbox))
			if err != nil {
				return nil
			}
			res.Flush()
		}
	}
}

func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
