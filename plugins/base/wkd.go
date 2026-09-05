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

	"git.mehdix.org/alborz"
)

// A Web Key Directory (draft-koch-openpgp-webkey-service) is the one
// key source that says more than the message does: the key is fetched
// from the sender's own domain over https, so the domain vouches for
// the address, the same trust delivery already rests on. A key server
// would be anyone's upload and says nothing, and is not asked.

const (
	// wkdTimeout bounds the whole lookup; a message page waits on it
	// once per author and then the cache answers for a day.
	wkdTimeout = 3 * time.Second
	// wkdKeepFor is how long a lookup's answer holds, the miss too, so
	// a domain without a directory costs one request a day.
	wkdKeepFor = 24 * time.Hour
	// wkdMaxSize bounds what a directory may hand back; a key with a
	// few certifications is a few kilobytes.
	wkdMaxSize = 256 << 10
)

// zbase32 is the alphabet the directory's file names use (RFC 6189 5.1.6).
const zbase32 = "ybndrfg8ejkmcpqxot1uwisza345h769"

type wkdEntry struct {
	keys    openpgp.EntityList
	fetched time.Time
}

type wkdCache struct {
	mu      sync.Mutex
	entries map[string]wkdEntry
	client  *http.Client
}

var wkd = &wkdCache{entries: map[string]wkdEntry{}, client: alborz.NewRemoteClient(wkdTimeout)}

// keys returns what the address's domain publishes for it, an empty
// list when nothing, from the cache when asked within the day.
func (w *wkdCache) keys(address string) openpgp.EntityList {
	address = strings.ToLower(address)
	w.mu.Lock()
	e, ok := w.entries[address]
	w.mu.Unlock()
	if ok && time.Since(e.fetched) < wkdKeepFor {
		return e.keys
	}
	keys := w.fetch(address)
	w.mu.Lock()
	w.entries[address] = wkdEntry{keys: keys, fetched: time.Now()}
	w.mu.Unlock()
	return keys
}

// fetch tries the advanced method, on a subdomain of its own, and then
// the direct one, the order the draft gives; the first answer wins.
func (w *wkdCache) fetch(address string) openpgp.EntityList {
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
		resp, err := w.client.Get(u)
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

// wkdHash names the file for a local part: the SHA-1 of its lowercase
// form in z-base-32, 32 characters for the 20 bytes.
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
