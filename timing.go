package alborz

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Timing accumulates per-request spans for the Server-Timing header.
type Timing struct {
	mu     sync.Mutex
	spans  map[string][]span
	counts map[string]int
}

type span struct {
	start, end time.Time
}

func NewTiming() *Timing {
	return &Timing{spans: make(map[string][]span), counts: make(map[string]int)}
}

// Add records one span of the named kind; DAV pages call it concurrently.
func (t *Timing) Add(kind string, start, end time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.spans[kind] = append(t.spans[kind], span{start, end})
	t.mu.Unlock()
}

// Header renders the spans as a Server-Timing value, kinds in a stable order,
// overlaps of one kind counted once. It returns "" when nothing was measured.
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
	t, ok := ctx.Value(TimingKey).(*Timing)
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

var TimingKey timingKeyType

// AddTiming records the span from start until now against kind, if reqCtx
// carries a Timing.
func AddTiming(reqCtx context.Context, kind string, start time.Time) {
	if t, ok := reqCtx.Value(TimingKey).(*Timing); ok {
		t.Add(kind, start, time.Now())
	}
}
