package alborzbase

import (
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// The listing cache holds each account's default folder view so a click
// renders from memory instead of refetching fifty envelopes. Entries are
// served as-is while fresh; after that a single STATUS decides between
// reuse and refetch. Every write through alborz evicts, so only changes
// made by other clients can go unnoticed, bounded by listingFreshFor.
const (
	// Served without asking the server at all.
	listingFreshFor = 30 * time.Second
	// Never reused past this, however quiet the folder looks.
	listingMaxAge = 5 * time.Minute
)

type listingState int

const (
	listingMiss listingState = iota
	listingStale
	listingFresh
)

// listingEntry is one cached folder view. Unified entries, keyed by role
// with a "#" prefix, carry no sidebar.
type listingEntry struct {
	sb            sidebar
	msgs          []IMAPMessage
	total         int
	perPage       int
	sortSupported bool
	snap          *imap.StatusData
	fetched       time.Time
	created       time.Time
}

type listingKey struct{ user, view string }

var listings = &listingCache{entries: make(map[listingKey]*listingEntry)}

type listingCache struct {
	mu      sync.Mutex
	entries map[listingKey]*listingEntry
}

// snapshot returns a private copy so the caller can assemble and tag it
// without racing other requests on the shared entry.
func (e *listingEntry) snapshot() *listingEntry {
	return &listingEntry{
		sb:            e.sb.clone(),
		msgs:          append([]IMAPMessage(nil), e.msgs...),
		total:         e.total,
		perPage:       e.perPage,
		sortSupported: e.sortSupported,
		snap:          e.snap,
	}
}

func (lc *listingCache) lookup(user, view string, perPage int) (*listingEntry, listingState) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	e, ok := lc.entries[listingKey{user, view}]
	if !ok || e.perPage != perPage || time.Since(e.created) > listingMaxAge {
		return nil, listingMiss
	}
	if time.Since(e.fetched) > listingFreshFor {
		return e.snapshot(), listingStale
	}
	return e.snapshot(), listingFresh
}

// store keeps its own copies, so the caller's data stays free to mutate.
func (lc *listingCache) store(user, view string, e *listingEntry) {
	kept := e.snapshot()
	now := time.Now()
	kept.fetched, kept.created = now, now

	lc.mu.Lock()
	lc.entries[listingKey{user, view}] = kept
	// Dead entries only waste memory; sweep them while the lock is held.
	for k, old := range lc.entries {
		if time.Since(old.created) > listingMaxAge {
			delete(lc.entries, k)
		}
	}
	lc.mu.Unlock()
}

// refresh marks the entry good again after a revalidation found no change.
func (lc *listingCache) refresh(user, view string) {
	lc.mu.Lock()
	if e, ok := lc.entries[listingKey{user, view}]; ok {
		e.fetched = time.Now()
	}
	lc.mu.Unlock()
}

// evict drops the user's entry for one folder, along with the unified
// entries, whose role names hide which folders they map to.
func (lc *listingCache) evict(user, folder string) {
	lc.mu.Lock()
	delete(lc.entries, listingKey{user, folder})
	for k := range lc.entries {
		if k.user == user && strings.HasPrefix(k.view, "#") {
			delete(lc.entries, k)
		}
	}
	lc.mu.Unlock()
}

// evictAll forgets everything cached for the user, for changes that reshape
// every view: sending, folder create or delete, settings, logout.
func (lc *listingCache) evictAll(user string) {
	lc.mu.Lock()
	for k := range lc.entries {
		if k.user == user {
			delete(lc.entries, k)
		}
	}
	lc.mu.Unlock()
}

// listingStatusOptions asks for the fields that betray a change to a
// folder's listing. HIGHESTMODSEQ also moves on flag changes made by other
// clients, but exists only with CONDSTORE.
func listingStatusOptions(c *imapclient.Client) *imap.StatusOptions {
	opts := &imap.StatusOptions{
		NumMessages: true,
		UIDNext:     true,
		UIDValidity: true,
		NumUnseen:   true,
	}
	if c.Caps().Has(imap.CapCondStore) {
		opts.HighestModSeq = true
	}
	return opts
}

func statusUnchanged(a, b *imap.StatusData) bool {
	if a == nil || b == nil {
		return false
	}
	eq := func(x, y *uint32) bool { return x != nil && y != nil && *x == *y }
	return a.UIDValidity == b.UIDValidity && a.UIDNext == b.UIDNext &&
		eq(a.NumMessages, b.NumMessages) && eq(a.NumUnseen, b.NumUnseen) &&
		a.HighestModSeq == b.HighestModSeq
}
