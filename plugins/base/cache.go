package alborzbase

import (
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// The listing cache holds each account's folder views so a click renders
// from memory instead of refetching fifty envelopes. What it holds is
// served as it is, however old: a stale entry is checked behind the page
// with one STATUS, and refetched only when the folder moved, so a change
// made elsewhere shows one visit late and never costs the click. Every
// write through alborz evicts, and the IDLE watcher evicts and refetches
// INBOX on what the server announces, so a watched INBOX is never stale
// and a change goes unnoticed only on a server without IDLE, bounded by
// listingFreshFor.
const (
	// Served without asking the server at all.
	listingFreshFor = 30 * time.Second
	// An entry nobody has read for this long is let go of.
	listingKeepFor = 24 * time.Hour
)

type listingState int

const (
	listingMiss listingState = iota
	listingStale
	listingFresh
)

// listingSep joins a folder to what narrows it inside a cache key. It
// cannot occur in a folder name, so a prefix match finds every view of
// one folder when the folder changes.
const listingSep = "\x00"

// maxListingEntries bounds a map whose keys include user input: a search
// makes an entry, so a crawler could otherwise mint them faster than the
// age sweep retires them.
const maxListingEntries = 512

// listingView names one cached view: the folder itself when nothing
// narrows it, otherwise the folder and the narrowing. A search is as
// cacheable as a plain listing; only its key is longer.
func listingView(folder, query string, starred bool, sortKey, sortDir string) string {
	if query == "" && !starred && sortKey == "" && sortDir == "" {
		return folder
	}
	starredMark := ""
	if starred {
		starredMark = "starred"
	}
	return strings.Join([]string{folder, query, starredMark, sortKey, sortDir}, listingSep)
}

// listingEntry is one cached folder view. Unified entries, keyed by role
// with a "#" prefix, carry no sidebar.
type listingEntry struct {
	sb            sidebar
	msgs          []IMAPMessage
	total         int
	perPage       int
	sortSupported bool
	// threadAlgorithm is what the server offered when the listing was
	// taken; without it a cached page forgets the folder can be read as
	// conversations and stops offering the view.
	threadAlgorithm imap.ThreadAlgorithm
	snap            *imap.StatusData
	fetched         time.Time
	lastUse         time.Time
}

type listingKey struct{ user, view string }

var listings = &listingCache{entries: make(map[listingKey]*listingEntry), refreshing: make(map[listingKey]bool)}

type listingCache struct {
	mu      sync.Mutex
	entries map[listingKey]*listingEntry
	// refreshing marks the views being checked behind a page, so one
	// stale entry read by many costs one STATUS.
	refreshing map[listingKey]bool
}

// listingSpec is what it takes to fetch a view again without the
// request that first asked for it.
type listingSpec struct {
	mbox    string
	query   string
	starred bool
	sortKey string
	sortDir string
	thread  imap.UID
}

func (spec listingSpec) reverse() bool {
	if spec.sortDir != "" {
		return spec.sortDir == "desc"
	}
	return sortKeys[spec.sortKey].descends
}

// snapshot returns a private copy so the caller can assemble and tag it
// without racing other requests on the shared entry.
func (e *listingEntry) snapshot() *listingEntry {
	return &listingEntry{
		sb:              e.sb.clone(),
		msgs:            append([]IMAPMessage(nil), e.msgs...),
		total:           e.total,
		perPage:         e.perPage,
		sortSupported:   e.sortSupported,
		threadAlgorithm: e.threadAlgorithm,
		snap:            e.snap,
	}
}

func (lc *listingCache) lookup(user, view string, perPage int) (*listingEntry, listingState) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	e, ok := lc.entries[listingKey{user, view}]
	if !ok || e.perPage != perPage {
		return nil, listingMiss
	}
	e.lastUse = time.Now()
	if time.Since(e.fetched) > listingFreshFor {
		return e.snapshot(), listingStale
	}
	return e.snapshot(), listingFresh
}

// store keeps its own copies, so the caller's data stays free to mutate.
func (lc *listingCache) store(user, view string, e *listingEntry) {
	kept := e.snapshot()
	now := time.Now()
	kept.fetched, kept.lastUse = now, now

	lc.mu.Lock()
	lc.entries[listingKey{user, view}] = kept
	// Dead entries only waste memory; sweep them while the lock is held.
	for k, old := range lc.entries {
		if time.Since(old.lastUse) > listingKeepFor {
			delete(lc.entries, k)
		}
	}
	// Whatever the sweep left, the least recently read go until the
	// map is in bounds.
	for len(lc.entries) > maxListingEntries {
		var oldestKey listingKey
		var oldest time.Time
		for k, e := range lc.entries {
			if oldest.IsZero() || e.lastUse.Before(oldest) {
				oldestKey, oldest = k, e.lastUse
			}
		}
		delete(lc.entries, oldestKey)
	}
	lc.mu.Unlock()
}

// markSeen records in every cached view of the folder that the message
// was read, so the listing stays true without being fetched again: the
// row's flag, and the folder's unseen count on the rail. The buffers
// are shared with pages being rendered, so a changed row gets a copy.
func (lc *listingCache) markSeen(user, folder string, uid imap.UID) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for k, e := range lc.entries {
		if k.user != user {
			continue
		}
		for i := range e.msgs {
			m := &e.msgs[i]
			if m.UID != uid || m.Mailbox != folder || m.HasFlag(imap.FlagSeen) {
				continue
			}
			buf := *m.FetchMessageBuffer
			buf.Flags = append(append([]imap.Flag(nil), buf.Flags...), imap.FlagSeen)
			m.FetchMessageBuffer = &buf
			e.sb.adjustUnseen(folder, -1)
		}
	}
}

// setFlags takes a flag change the server announced into the cached
// views of the folder: the row's flags, and the unseen count when
// \Seen came or went. A message on no cached page is not shown, so
// there is nothing to keep true for it.
func (lc *listingCache) setFlags(user, folder string, seqNum uint32, flags []imap.Flag) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for k, e := range lc.entries {
		if k.user != user {
			continue
		}
		for i := range e.msgs {
			m := &e.msgs[i]
			if m.Mailbox != folder || m.SeqNum != seqNum {
				continue
			}
			wasSeen := m.HasFlag(imap.FlagSeen)
			buf := *m.FetchMessageBuffer
			buf.Flags = append([]imap.Flag(nil), flags...)
			m.FetchMessageBuffer = &buf
			if seen := m.HasFlag(imap.FlagSeen); seen != wasSeen {
				delta := 1
				if seen {
					delta = -1
				}
				e.sb.adjustUnseen(folder, delta)
			}
		}
	}
}

// row is the cached row of the message, nil when off the page.
func (e *listingEntry) row(uid imap.UID) *IMAPMessage {
	for i := range e.msgs {
		if e.msgs[i].UID == uid {
			return &e.msgs[i]
		}
	}
	return nil
}

// neighbours places the message in the cached page: the rows either
// side of it, its position and the folder's total. Not found means the
// server has to say, at the page's far edge as much as off the page.
func (e *listingEntry) neighbours(uid imap.UID) (newer, older imap.UID, pos, total int, found bool) {
	for i := range e.msgs {
		if e.msgs[i].UID != uid {
			continue
		}
		if i+1 == len(e.msgs) && e.total > len(e.msgs) {
			return 0, 0, 0, 0, false
		}
		if i > 0 {
			newer = e.msgs[i-1].UID
		}
		if i+1 < len(e.msgs) {
			older = e.msgs[i+1].UID
		}
		return newer, older, i + 1, e.total, true
	}
	return 0, 0, 0, 0, false
}

// claim marks the view as being checked; false means someone already is.
func (lc *listingCache) claim(user, view string) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	k := listingKey{user, view}
	if lc.refreshing[k] {
		return false
	}
	lc.refreshing[k] = true
	return true
}

func (lc *listingCache) release(user, view string) {
	lc.mu.Lock()
	delete(lc.refreshing, listingKey{user, view})
	lc.mu.Unlock()
}

// revalidateListing asks, behind the page, whether the folder moved
// since the entry was taken, and refetches the view when it did.
func revalidateListing(s *alborz.Session, settings *Settings, view string, spec listingSpec, e *listingEntry) {
	user := s.Username()
	if !listings.claim(user, view) {
		return
	}
	go func() {
		defer listings.release(user, view)
		var fresh *listingEntry
		err := s.DoIMAP(func(c *imapclient.Client) error {
			st, err := c.Status(spec.mbox, listingStatusOptions(c)).Wait()
			if err != nil {
				return err
			}
			if statusUnchanged(e.snap, st) {
				listings.refresh(user, view)
				return nil
			}
			fresh, err = fetchListing(c, spec, settings, 0, e.perPage)
			return err
		})
		if err == nil && fresh != nil {
			listings.store(user, view, fresh)
		}
	}()
}

// revalidateUnified is revalidateListing for one account's share of a
// merged view, whose folder is found by role.
func revalidateUnified(s *alborz.Session, settings *Settings, view, role string, spec listingSpec, e *listingEntry) {
	user := s.Username()
	if !listings.claim(user, view) {
		return
	}
	go func() {
		defer listings.release(user, view)
		var fresh *listingEntry
		err := s.DoIMAP(func(c *imapclient.Client) error {
			folder, err := resolveRole(c, user, role)
			if err != nil || folder == "" {
				return err
			}
			st, err := c.Status(folder, listingStatusOptions(c)).Wait()
			if err != nil {
				return err
			}
			if statusUnchanged(e.snap, st) {
				listings.refresh(user, view)
				return nil
			}
			fresh, err = fetchUnifiedAccount(c, user, folder, spec, settings, e.perPage, true)
			return err
		})
		if err == nil && fresh != nil && fresh.snap != nil {
			listings.store(user, view, fresh)
		}
	}()
}

// refresh marks the entry good again after a revalidation found no change.
func (lc *listingCache) refresh(user, view string) {
	lc.mu.Lock()
	if e, ok := lc.entries[listingKey{user, view}]; ok {
		e.fetched = time.Now()
	}
	lc.mu.Unlock()
}

// evict drops every cached view of one folder - the plain listing and
// each search or sort over it - along with the unified entries, whose
// role names hide which folders they map to.
func (lc *listingCache) evict(user, folder string) {
	lc.mu.Lock()
	for k := range lc.entries {
		if k.user != user {
			continue
		}
		if k.view == folder || strings.HasPrefix(k.view, folder+listingSep) ||
			strings.HasPrefix(k.view, "#") {
			delete(lc.entries, k)
		}
	}
	lc.mu.Unlock()
	// The aside shows the same counts the listings do.
	accountSidebars.Forget(user)
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
	accountSidebars.Forget(user)
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
