package alborz

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-smtp"
)

// Timing accumulates per-request upstream and render spans, reported back in
// the Server-Timing response header so the browser's network panel
// attributes a slow page to IMAP, ManageSieve, WebDAV, or rendering.
type Timing struct {
	mu    sync.Mutex
	spans map[string][]span
}

type span struct {
	start, end time.Time
}

func newTiming() *Timing {
	return &Timing{spans: make(map[string][]span)}
}

// add records one span of the named kind ("imap", "sieve", "smtp", "dav",
// "render"). Concurrent callers are safe: DAV pages query collections in
// parallel.
func (t *Timing) add(kind string, start, end time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.spans[kind] = append(t.spans[kind], span{start, end})
	t.mu.Unlock()
}

// Header renders the accumulated spans as a Server-Timing value, kinds in a
// stable order. Overlapping spans of one kind count once, so concurrent
// upstream requests report wall time, never more than the request took. It
// returns "" when nothing was measured.
func (t *Timing) Header() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	kinds := make([]string, 0, len(t.spans))
	for kind := range t.spans {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		ms := float64(mergedDuration(t.spans[kind])) / float64(time.Millisecond)
		parts = append(parts, fmt.Sprintf("%s;dur=%.1f", kind, ms))
	}
	return strings.Join(parts, ", ")
}

// mergedDuration sums spans with overlaps counted once.
func mergedDuration(spans []span) time.Duration {
	sort.Slice(spans, func(i, j int) bool {
		return spans[i].start.Before(spans[j].start)
	})
	var total time.Duration
	cur := spans[0]
	for _, s := range spans[1:] {
		if s.start.After(cur.end) {
			total += cur.end.Sub(cur.start)
			cur = s
			continue
		}
		if s.end.After(cur.end) {
			cur.end = s.end
		}
	}
	return total + cur.end.Sub(cur.start)
}

type timingKeyType struct{}

var timingKey timingKeyType

// AddTiming records the span from start until now against kind on the Timing
// carried by reqCtx, if any. It lets plugins measuring WebDAV requests
// through the request context feed the same accumulator without importing
// the request plumbing.
func AddTiming(reqCtx context.Context, kind string, start time.Time) {
	if t, ok := reqCtx.Value(timingKey).(*Timing); ok {
		t.add(kind, start, time.Now())
	}
}

// DoIMAP runs the IMAP operation and attributes its duration to the request.
func (ctx *Context) DoIMAP(f func(*imapclient.Client) error) error {
	start := time.Now()
	err := ctx.Session.DoIMAP(f)
	ctx.timing.add("imap", start, time.Now())
	return err
}

// DoSieve runs the ManageSieve operation and attributes its duration.
func (ctx *Context) DoSieve(f func(SieveClient) error) error {
	start := time.Now()
	err := ctx.Session.DoSieve(f)
	ctx.timing.add("sieve", start, time.Now())
	return err
}

// DoSMTP runs the SMTP operation and attributes its duration.
func (ctx *Context) DoSMTP(f func(*smtp.Client) error) error {
	start := time.Now()
	err := ctx.Session.DoSMTP(f)
	ctx.timing.add("smtp", start, time.Now())
	return err
}

// installTiming attaches a fresh Timing to the context and to the request
// context so plugin round-trippers reach it, and arranges the header to be
// written just before the response.
func (ctx *Context) installTiming() {
	ctx.timing = newTiming()
	req := ctx.Request()
	ctx.SetRequest(req.WithContext(context.WithValue(req.Context(), timingKey, ctx.timing)))
	ctx.Response().Before(func() {
		if h := ctx.timing.Header(); h != "" {
			ctx.Response().Header().Set("Server-Timing", h)
		}
	})
}
