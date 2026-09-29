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

// timing accumulates per-request spans for the Server-timing header.
type timing struct {
	mu     sync.Mutex
	spans  map[string][]span
	counts map[string]int
}

type span struct {
	start, end time.Time
}

func newTiming() *timing {
	return &timing{spans: make(map[string][]span), counts: make(map[string]int)}
}

// add records one span of the named kind; DAV pages call it concurrently.
func (t *timing) add(kind string, start, end time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.spans[kind] = append(t.spans[kind], span{start, end})
	t.mu.Unlock()
}

// header renders the spans as a Server-Timing value, kinds in a stable order,
// overlaps of one kind counted once. It returns "" when nothing was measured.
func (t *timing) header() string {
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
	kinds = kinds[:0]
	for kind := range t.counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		parts = append(parts, fmt.Sprintf("%s;desc=\"%d\"", kind, t.counts[kind]))
	}
	return strings.Join(parts, ", ")
}

// CacheTiming records a cache hit or miss.
func CacheTiming(ctx context.Context, cache string, hit bool) {
	t, ok := ctx.Value(timingKey).(*timing)
	if !ok {
		return
	}
	outcome := "_miss"
	if hit {
		outcome = "_hit"
	}
	t.mu.Lock()
	t.counts[cache+outcome]++
	t.mu.Unlock()
}

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

// AddTiming records the span from start until now against kind, if reqCtx
// carries a Timing.
func AddTiming(reqCtx context.Context, kind string, start time.Time) {
	if t, ok := reqCtx.Value(timingKey).(*timing); ok {
		t.add(kind, start, time.Now())
	}
}

// DoIMAP runs the IMAP operation and times it.
func (ctx *Context) DoIMAP(f func(*imapclient.Client) error) error {
	return ctx.doIMAPWithin(RoundTripTimeout, f)
}

// DoIMAPScan runs a whole-folder search, which may outlast a round trip, off the connection the reader's clicks wait on.
func (ctx *Context) DoIMAPScan(f func(*imapclient.Client) error) error {
	return ctx.Login.DoIMAPWork(ctx.Request().Context(), IMAPScan, ScanTimeout, f)
}

func (ctx *Context) doIMAPWithin(bound time.Duration, f func(*imapclient.Client) error) error {
	start := time.Now()
	err := ctx.Login.doIMAPContext(ctx.Request().Context(), bound, f)
	ctx.timing.add("imap", start, time.Now())
	return err
}

// DoSieve runs the ManageSieve operation and times it.
func (ctx *Context) DoSieve(f func(SieveClient) error) error {
	start := time.Now()
	err := ctx.Login.DoSieve(f)
	ctx.timing.add("sieve", start, time.Now())
	return err
}

// DoSMTP runs the SMTP operation and times it.
func (ctx *Context) DoSMTP(f func(*smtp.Client) error) error {
	start := time.Now()
	err := ctx.Login.DoSMTP(f)
	ctx.timing.add("smtp", start, time.Now())
	return err
}

// installTiming also puts the Timing on the request context, so plugin
// round-trippers reach it.
func (ctx *Context) installTiming() {
	ctx.timing = newTiming()
	req := ctx.Request()
	ctx.SetRequest(req.WithContext(context.WithValue(req.Context(), timingKey, ctx.timing)))
	ctx.Response().Before(func() {
		if h := ctx.timing.header(); h != "" {
			ctx.Response().Header().Set("Server-Timing", h)
		}
	})
}
