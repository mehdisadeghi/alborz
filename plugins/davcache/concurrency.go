package davcache

import (
	"context"
	"io"
	"net/http"
	"sync"
)

// Half the per-host capacity is reserved for foreground misses and writes.
const (
	connectionsPerHost = 4
	backgroundPerHost  = 2
	refreshWorkers     = 4
)

type hostLimit struct{ all, background chan struct{} }

type hostLimits struct {
	mu    sync.Mutex
	hosts map[string]*hostLimit
}

func (p *hostLimits) host(name string) *hostLimit {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.hosts[name]
	if h == nil {
		h = &hostLimit{make(chan struct{}, connectionsPerHost), make(chan struct{}, backgroundPerHost)}
		p.hosts[name] = h
	}
	return h
}

type foregroundKey struct{}

// replayKey marks a request as one the cache sends of its own accord.
type replayKey struct{}

// replayed is the cache's own client: what it sends is background work
// to the limiter, unless a reader is waiting on it.
type replayed struct{ next http.RoundTripper }

func (t replayed) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.next.RoundTrip(req.WithContext(context.WithValue(req.Context(), replayKey{}, true)))
}

// Limit bounds what next sends to one host at a time. It goes below
// whatever decides the host a request leaves for: above it, every
// server behind one routed address would share that address's share.
func (c *Cache) Limit(next http.RoundTripper) http.RoundTripper {
	return limitedTransport{next: next, limits: c.limits}
}

type limitedTransport struct {
	next   http.RoundTripper
	limits *hostLimits
}

func (t limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	h := t.limits.host(req.URL.Scheme + "://" + req.URL.Host)
	foreground, _ := req.Context().Value(foregroundKey{}).(bool)
	replay, _ := req.Context().Value(replayKey{}).(bool)
	background := replay && !foreground
	if background {
		select {
		case h.background <- struct{}{}:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	releaseBackground := func() {
		if background {
			<-h.background
		}
	}
	select {
	case h.all <- struct{}{}:
	case <-req.Context().Done():
		releaseBackground()
		return nil, req.Context().Err()
	}
	release := func() { <-h.all; releaseBackground() }
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		release()
		return nil, err
	}
	resp.Body = &limitedBody{ReadCloser: resp.Body, release: release}
	return resp, nil
}

type limitedBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *limitedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.release() })
	return err
}

func parallel[T any](values []T, f func(T)) {
	jobs := make(chan T)
	var wg sync.WaitGroup
	for range min(refreshWorkers, len(values)) {
		wg.Go(func() {
			for v := range jobs {
				f(v)
			}
		})
	}
	for _, v := range values {
		jobs <- v
	}
	close(jobs)
	wg.Wait()
}
