package alborzbase

import (
	"context"
	"net/http"
	"sync"
	"time"

	"git.mehdix.org/alborz/internal/pgp"
	"github.com/ProtonMail/go-crypto/openpgp"
)

// Only WKD is asked: the sender's own domain vouches for the key, where a key server holds anyone's upload.

const (
	// A message page waits on the lookup once per author.
	wkdTimeout = 3 * time.Second
	// Misses are kept too, so a domain without a directory costs one request a day.
	wkdKeepFor = 24 * time.Hour
)

type wkdEntry struct {
	keys    openpgp.EntityList
	fetched time.Time
}

type wkdCache struct {
	keepFor time.Duration
	mu      sync.Mutex
	entries map[string]wkdEntry
}

func newWKDCache(keepFor time.Duration) *wkdCache {
	return &wkdCache{keepFor: keepFor, entries: map[string]wkdEntry{}}
}

func (w *wkdCache) keys(ctx context.Context, client *http.Client, address string) openpgp.EntityList {
	w.mu.Lock()
	e, ok := w.entries[address]
	w.mu.Unlock()
	if ok && time.Since(e.fetched) < w.keepFor {
		return e.keys
	}
	keys, _ := pgp.WKD(ctx, client, address)
	// A reader who left says nothing about the domain.
	if w.keepFor == 0 || ctx.Err() != nil {
		return keys
	}
	w.mu.Lock()
	w.entries[address] = wkdEntry{keys: keys, fetched: time.Now()}
	w.mu.Unlock()
	return keys
}
