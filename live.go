package alborz

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Change is something that happened to an account without anyone asking:
// mail arrived, or a folder moved under a reader who was looking
// elsewhere. It carries the fact and nothing rendered, because the
// language it is said in belongs to whoever is reading, not to the
// watcher that noticed.
type Change struct {
	Account string
	Mailbox string
}

// Changes is who to tell. A watcher publishes to it and every stream
// open on the account hears; a stream that nobody is reading is dropped
// rather than waited for, since a change is worth nothing late.
type Changes struct {
	mu   sync.Mutex
	subs map[chan Change]struct{}
}

func newChanges() *Changes {
	return &Changes{subs: map[chan Change]struct{}{}}
}

// Listen returns a channel of changes and the function that closes it.
// The buffer holds a few, so a reader that is mid-render does not miss
// the one that follows.
func (c *Changes) Listen() (<-chan Change, func()) {
	ch := make(chan Change, 8)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
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

// Publish tells every listener. It never blocks: a full channel means a
// browser that is not keeping up, and the change it misses is followed
// by the next one.
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

// Announce says a folder of an account is not what a page last showed.
// The watcher that sits in IDLE calls it; it knows the account and the
// folder and nothing about who is reading.
func (c *Changes) Announce(account, mailbox string) {
	c.Publish(Change{Account: account, Mailbox: mailbox})
}

// livePing is how often the stream says nothing, so a proxy between the
// reader and alborz does not decide the connection is dead. A comment
// frame is legal SSE and costs two bytes.
const livePing = 25 * time.Second

// handleEvents streams what the watchers noticed to one browser, as
// server-sent events. It renders nothing: the page it reaches decides
// what a change means where the reader is standing, and a reader with
// no script never opens it at all.
func handleEvents(ctx *Context) error {
	mine := map[string]bool{}
	for _, s := range ctx.Accounts() {
		mine[s.Username] = true
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
			if !mine[change.Account] {
				continue
			}
			// One line each, and no newline can reach them: an
			// address and a folder name are the only values, and a
			// folder name with a newline in it would end the frame.
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
