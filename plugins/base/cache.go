package alborzbase

import (
	"context"
	"fmt"
	"slices"
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
// write through alborz evicts, and the IDLE watcher marks INBOX stale
// and refetches its first page on what the server announces, so a change
// goes unnoticed only on a server without IDLE, bounded by
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
	// listingChanged is an entry the watcher saw the folder move under:
	// it is still there to be read in place, and no page is drawn from
	// it.
	listingChanged
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
func listingView(folder, query, view, sortKey, sortDir string) string {
	if query == "" && view == "" && sortKey == "" && sortDir == "" {
		return folder
	}
	return strings.Join([]string{folder, query, view, sortKey, sortDir}, listingSep)
}

// listingEntry is one cached folder view. Unified entries, keyed by role
// with a "#" prefix, carry no sidebar.
type listingEntry struct {
	sb            sidebar
	msgs          []IMAPMessage
	total         int
	perPage       int
	page          int
	sortSupported bool
	// threadAlgorithm is what the server offered when the listing was
	// taken; without it a cached page forgets the folder can be read as
	// conversations and stops offering the view.
	threadAlgorithm imap.ThreadAlgorithm
	// headersOnly says a search's bare terms reached the headers and
	// not the message, which is what the page offers to widen.
	headersOnly bool
	snap        *imap.StatusData
	fetched     time.Time
	lastUse     time.Time
	// changed says the watcher saw the folder move, as against an entry
	// that merely aged: what it holds is known to be wrong, so it is not
	// shown while a revalidation runs behind the page.
	changed bool
}

type listingKey struct{ user, view string }

var listings = newListingCache()

func newListingCache() *listingCache {
	return &listingCache{
		entries: make(map[listingKey]*listingEntry), refreshing: make(map[listingKey]bool),
		flights: make(map[listingKey]*listingFlight), generation: make(map[string]uint64),
	}
}

type listingCache struct {
	mu      sync.Mutex
	entries map[listingKey]*listingEntry
	// refreshing marks the views being checked behind a page, so one
	// stale entry read by many costs one STATUS.
	refreshing map[listingKey]bool
	flights    map[listingKey]*listingFlight
	generation map[string]uint64
}

type listingFlight struct {
	done  chan struct{}
	entry *listingEntry
	err   error
	// queued is a speculative fetch not yet on the connection: a reader
	// joining it withdraws it and fetches the view itself.
	queued   bool
	withdraw context.CancelFunc
	takeover func() (*listingEntry, error)
}

func listingPage(view string, page, perPage int) string {
	if page == 0 {
		return view
	}
	return fmt.Sprintf("%s%spage=%d&size=%d", view, listingSep, page, perPage)
}

func (e *listingEntry) validity() uint32 {
	if e.snap == nil {
		return 0
	}
	return e.snap.UIDValidity
}

// load coalesces cold reads, while callers can leave independently. A
// reader never waits behind speculative work: it joins a speculative
// fetch only once that fetch is on the connection.
func (lc *listingCache) load(ctx context.Context, user, view string, perPage int, speculative bool, fetch func(context.Context) (*listingEntry, error)) (*listingEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lc.mu.Lock()
	generation := lc.generation[user]
	k := listingKey{user, fmt.Sprintf("%s%ssize=%d&generation=%d", view, listingSep, perPage, generation)}
	f := lc.flights[k]
	if f == nil {
		f = &listingFlight{done: make(chan struct{}), queued: speculative}
		// Only the shared deadline and the session lifetime own the fetch.
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), alborz.ScanTimeout)
		f.withdraw = cancel
		work = alborz.WithIMAPStart(work, func() bool {
			lc.mu.Lock()
			defer lc.mu.Unlock()
			f.queued = false
			return f.takeover == nil
		})
		lc.flights[k] = f
		go func() {
			defer cancel()
			if e, state := lc.lookup(user, view, perPage); e != nil && state == listingFresh {
				f.entry = e
			} else {
				f.entry, f.err = fetch(work)
				lc.mu.Lock()
				f.queued = false
				takeover := f.takeover
				lc.mu.Unlock()
				if takeover != nil {
					f.entry, f.err = takeover()
				}
				if f.err == nil && f.entry != nil {
					lc.storeAt(user, view, f.entry, generation)
				}
			}
			lc.mu.Lock()
			delete(lc.flights, k)
			close(f.done)
			lc.mu.Unlock()
		}()
	} else if f.queued && !speculative {
		f.queued = false
		f.withdraw()
		f.takeover = func() (*listingEntry, error) {
			work, cancel := context.WithTimeout(context.WithoutCancel(ctx), alborz.ScanTimeout)
			defer cancel()
			return fetch(work)
		}
	}
	lc.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.done:
		if f.entry == nil {
			return nil, f.err
		}
		return f.entry.snapshot(), f.err
	}
}

func (lc *listingCache) epoch(user string) uint64 {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.generation[user]
}

// message finds a recently visited page containing this message, and
// its place in the view: the page's rows either side of it, or across
// the page's edge the rows of the held page next to it.
func (lc *listingCache) message(user, view string, uid imap.UID, perPage int) (*listingEntry, listPlace, bool) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	var found *listingEntry
	for k, e := range lc.entries {
		if k.user != user || !(k.view == view || strings.HasPrefix(k.view, view+listingSep+"page=")) || e.perPage != perPage || e.row(uid) == nil {
			continue
		}
		if found == nil || e.fetched.After(found.fetched) {
			found = e
		}
	}
	if found == nil {
		return nil, listPlace{}, false
	}
	found.lastUse = time.Now()
	held := func(page int) *listingEntry {
		if e := lc.entries[listingKey{user, listingPage(view, page, perPage)}]; e != nil && e.perPage == perPage && found.adjoins(e) {
			return e
		}
		return nil
	}
	var place listPlace
	var placed bool
	place.newer, place.older, place.position, place.total, placed = found.neighbours(uid, held(found.page-1), held(found.page+1))
	return found.snapshot(), place, placed
}

// listingSpec is what it takes to fetch a view again without the
// request that first asked for it.
type listingSpec struct {
	mbox    string
	query   string
	view    string
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

// absorb adds what another folder or account answered to a merge of
// them: a merge sorts only where all of them do, and reached headers
// only where any of them did.
func (e *listingEntry) absorb(other *listingEntry) {
	e.msgs = append(e.msgs, other.msgs...)
	e.total += other.total
	e.headersOnly = e.headersOnly || other.headersOnly
	e.sortSupported = e.sortSupported && other.sortSupported
}

// snapshot returns a private copy so the caller can assemble and tag it
// without racing other requests on the shared entry.
func (e *listingEntry) snapshot() *listingEntry {
	return &listingEntry{
		sb:              e.sb.clone(),
		msgs:            append([]IMAPMessage(nil), e.msgs...),
		total:           e.total,
		perPage:         e.perPage,
		page:            e.page,
		sortSupported:   e.sortSupported,
		threadAlgorithm: e.threadAlgorithm,
		snap:            e.snap,
		headersOnly:     e.headersOnly,
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
	if e.changed {
		return e.snapshot(), listingChanged
	}
	if time.Since(e.fetched) > listingFreshFor {
		return e.snapshot(), listingStale
	}
	return e.snapshot(), listingFresh
}

// pageSize is the size of the view's held page, or fallback when none
// is held.
func (lc *listingCache) pageSize(user, view string, fallback int) int {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if e, ok := lc.entries[listingKey{user, view}]; ok {
		return e.perPage
	}
	return fallback
}

// store keeps its own copies, so the caller's data stays free to mutate.
func (lc *listingCache) store(user, view string, e *listingEntry) {
	lc.storeAt(user, view, e, lc.epoch(user))
}

func (lc *listingCache) storeAt(user, view string, e *listingEntry, generation uint64) {
	kept := e.snapshot()
	now := time.Now()
	kept.fetched, kept.lastUse, kept.changed = now, now, false
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.generation[user] != generation {
		return
	}
	// The rail draws from the memo, and this listing just counted.
	if kept.sb.mailboxes != nil {
		accountSidebars.Put(user, kept.sb.clone())
	}

	if kept.snap != nil {
		bodies.observe(user, kept.snap.Mailbox, kept.snap.UIDValidity)
	}
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
}

// markSeen records in every cached view of the folder that the message
// was read, so the listing stays true without being fetched again: the
// row's flag, and the folder's unseen count on the rail. The buffers
// are shared with pages being rendered, so a changed row gets a copy.
func (lc *listingCache) markSeen(user, folder string, uid imap.UID) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	// The rail moves once, however many views hold the row.
	moved := false
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
			moved = true
		}
	}
	if moved {
		railUnseen(user, folder, -1)
	}
}

// railUnseen moves a folder's count on the rail the way a listing's
// own copy is moved, so the two never disagree between reloads.
func railUnseen(user, folder string, delta int) {
	accountSidebars.Update(user, func(sb sidebar) sidebar {
		sb = sb.clone()
		sb.adjustUnseen(folder, delta)
		return sb
	})
}

// setFlags takes a flag change the server announced into the cached
// views of the folder: the row's flags, and the unseen count when
// \Seen came or went. A message on no cached page is not shown, so
// there is nothing to keep true for it.
//
// The server names the message by sequence number, which an expunge
// renumbers. A stale page was read before one, so its numbers name
// other messages now and it is left to renew.
func (lc *listingCache) setFlags(user, folder string, seqNum uint32, flags []imap.Flag) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	moved := 0
	for k, e := range lc.entries {
		if k.user != user || e.fetched.IsZero() {
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
			bodies.flags(user, folder, []imap.UID{m.UID}, imap.StoreFlagsSet, flags)
			if seen := m.HasFlag(imap.FlagSeen); seen != wasSeen {
				delta := 1
				if seen {
					delta = -1
				}
				e.sb.adjustUnseen(folder, delta)
				moved = delta
			}
		}
	}
	if moved != 0 {
		railUnseen(user, folder, moved)
	}
}

// A confirmed flag edit updates plain pages in place. Filtered views are
// invalidated because their membership may have changed.
func (lc *listingCache) flags(user, folder string, uids []imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) {
	among := uidSet(uids)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.generation[user]++
	deltas := make(map[imap.UID]int)
	shown := make(map[imap.UID]bool)
	for k, e := range lc.entries {
		if k.user != user {
			continue
		}
		if strings.HasPrefix(k.view, "#") || strings.HasPrefix(k.view, folder+listingSep) && !strings.HasPrefix(k.view, folder+listingSep+"page=") {
			delete(lc.entries, k)
			continue
		}
		if k.view != folder && !strings.HasPrefix(k.view, folder+listingSep+"page=") {
			continue
		}
		for i := range e.msgs {
			m := &e.msgs[i]
			if !among[m.UID] {
				continue
			}
			shown[m.UID] = true
			wasSeen := m.HasFlag(imap.FlagSeen)
			buf := *m.FetchMessageBuffer
			buf.Flags = changedFlags(buf.Flags, op, flags)
			m.FetchMessageBuffer = &buf
			if wasSeen != m.HasFlag(imap.FlagSeen) {
				delta := 1
				if m.HasFlag(imap.FlagSeen) {
					delta = -1
				}
				e.sb.adjustUnseen(folder, delta)
				deltas[m.UID] = delta
			}
		}
		e.fetched = time.Time{}
	}
	for _, delta := range deltas {
		railUnseen(user, folder, delta)
	}
	// A whole view marked read reaches messages no cached page shows,
	// and what they did to the unseen count only the server knows.
	if len(shown) < len(uids) && (op == imap.StoreFlagsSet || slices.Contains(flags, imap.FlagSeen)) {
		accountSidebars.Stale(user)
	}
	bodies.flags(user, folder, uids, op, flags)
}

// uidSet is a selection to look messages up in. A whole view can be a
// hundred thousand UIDs, and the caches are walked under locks every
// request of every reader waits on.
func uidSet(uids []imap.UID) map[imap.UID]bool {
	set := make(map[imap.UID]bool, len(uids))
	for _, uid := range uids {
		set[uid] = true
	}
	return set
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
// side of it, its position and the folder's total. At the page's edge
// the neighbour is the facing row of the page before or after, nil when
// not held; not found means the server has to say.
func (e *listingEntry) neighbours(uid imap.UID, before, after *listingEntry) (newer, older imap.UID, pos, total int, found bool) {
	i := slices.IndexFunc(e.msgs, func(m IMAPMessage) bool { return m.UID == uid })
	if i < 0 {
		return 0, 0, 0, 0, false
	}
	switch {
	case i > 0:
		newer = e.msgs[i-1].UID
	case e.page == 0:
	case before != nil && len(before.msgs) > 0:
		newer = before.msgs[len(before.msgs)-1].UID
	default:
		return 0, 0, 0, 0, false
	}
	switch {
	case i+1 < len(e.msgs):
		older = e.msgs[i+1].UID
	case e.total <= e.page*e.perPage+len(e.msgs):
	case after != nil && len(after.msgs) > 0:
		older = after.msgs[0].UID
	default:
		return 0, 0, 0, 0, false
	}
	return newer, older, e.page*e.perPage + i + 1, e.total, true
}

// adjoins says the other page was cut from the same list as this one:
// taken while the folder held the same messages, so no row slid across
// the edge between them. An arrival moves UIDNEXT, an expunge the total.
func (e *listingEntry) adjoins(other *listingEntry) bool {
	return e.snap != nil && other.snap != nil && e.total == other.total &&
		e.snap.UIDValidity == other.snap.UIDValidity && e.snap.UIDNext == other.snap.UIDNext
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

func (lc *listingCache) stale(user, folder string) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.generation[user]++
	for k, e := range lc.entries {
		if k.user == user && (k.view == folder || strings.HasPrefix(k.view, folder+listingSep) || strings.HasPrefix(k.view, "#")) {
			e.fetched, e.changed = time.Time{}, true
		}
	}
}

// revalidate asks, behind the page, whether the folder moved since the
// entry was taken, and reads the view again when it did. folder names
// it on the connection, since a merged view's is found by role there;
// fetch is the read that made the entry.
func revalidate(s *alborz.Session, view string, e *listingEntry, folder func(*imapclient.Client) (string, error), fetch func(*imapclient.Client) (*listingEntry, error)) {
	user := s.Username()
	if !listings.claim(user, view) {
		return
	}
	generation := listings.epoch(user)
	go func() {
		defer listings.release(user, view)
		var fresh *listingEntry
		err := s.DoIMAPBackground(func(c *imapclient.Client) error {
			name, err := folder(c)
			if err != nil || name == "" {
				return err
			}
			st, err := c.Status(name, listingStatusOptions(c)).Wait()
			if err != nil {
				return err
			}
			if statusUnchanged(e.snap, st) {
				listings.refresh(user, view)
				return nil
			}
			fresh, err = fetch(c)
			return err
		})
		if err == nil && fresh != nil && fresh.snap != nil {
			listings.storeAt(user, view, fresh, generation)
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
	lc.generation[user]++
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
	accountSidebars.Stale(user)
}

// evictAll forgets everything cached for the user, for changes that reshape
// every view: sending, folder create or delete, settings, logout.
func (lc *listingCache) evictAll(user string) {
	lc.mu.Lock()
	lc.generation[user]++
	for k := range lc.entries {
		if k.user == user {
			delete(lc.entries, k)
		}
	}
	lc.mu.Unlock()
	accountSidebars.Stale(user)
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
