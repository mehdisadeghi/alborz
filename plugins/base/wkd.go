package alborzbase

import (
	"crypto/sha1"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// Only WKD is asked: the sender's own domain vouches for the key, where a key server holds anyone's upload.

const (
	// A message page waits on the lookup once per author.
	wkdTimeout = 3 * time.Second
	// Misses are kept too, so a domain without a directory costs one request a day.
	wkdKeepFor = 24 * time.Hour
	// A key with a few certifications is a few kilobytes.
	wkdMaxSize = 256 << 10
)

// zbase32 is the z-base-32 alphabet of WKD file names.
const zbase32 = "ybndrfg8ejkmcpqxot1uwisza345h769"

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

func (w *wkdCache) keys(client *http.Client, address string) openpgp.EntityList {
	address = strings.ToLower(address)
	w.mu.Lock()
	e, ok := w.entries[address]
	w.mu.Unlock()
	if ok && time.Since(e.fetched) < w.keepFor {
		return e.keys
	}
	keys := w.fetch(client, address)
	if w.keepFor == 0 {
		return keys
	}
	w.mu.Lock()
	w.entries[address] = wkdEntry{keys: keys, fetched: time.Now()}
	w.mu.Unlock()
	return keys
}

// Advanced method first, then direct, as the WKD draft orders them.
func (w *wkdCache) fetch(client *http.Client, address string) openpgp.EntityList {
	local, domain, ok := strings.Cut(address, "@")
	if !ok || local == "" || domain == "" {
		return nil
	}
	hash := wkdHash(local)
	query := "?l=" + url.QueryEscape(local)
	for _, u := range []string{
		fmt.Sprintf("https://openpgpkey.%s/.well-known/openpgpkey/%s/hu/%s%s", domain, domain, hash, query),
		fmt.Sprintf("https://%s/.well-known/openpgpkey/hu/%s%s", domain, hash, query),
	} {
		resp, err := client.Get(u)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, wkdMaxSize))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		keys, err := openpgp.ReadKeyRing(strings.NewReader(string(body)))
		if err != nil {
			continue
		}
		return keys
	}
	return nil
}

func wkdHash(local string) string {
	sum := sha1.Sum([]byte(strings.ToLower(local)))
	var out strings.Builder
	var acc, bits uint
	for _, b := range sum {
		acc = acc<<8 | uint(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out.WriteByte(zbase32[(acc>>bits)&31])
		}
	}
	return out.String()
}
