package alborzbase

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// A stale listing is served, then checked behind the page with one STATUS.
const (
	listingFreshFor = 30 * time.Second
	listingKeepFor  = 24 * time.Hour
)

type listingState int

const (
	listingMiss listingState = iota
	// The watcher saw the folder move under it; no page is drawn from it.
	listingChanged
	listingStale
	listingFresh
)

// It cannot occur in a folder name, so a prefix finds every view of one.
const listingSep = "\x00"

// Every search makes an entry.
const maxListingEntries = 512

func listingView(folder, query, view, sortKey, sortDir string) string {
	if query == "" && view == "" && sortKey == "" && sortDir == "" {
		return folder
	}
	return strings.Join([]string{folder, query, view, sortKey, sortDir}, listingSep)
}

// Merged entries are keyed by role with a "#" prefix.
type listingEntry struct {
	sb              sidebar
	msgs            []IMAPMessage
	total           int
	perPage         int
	page            int
	sortSupported   bool
	threadAlgorithm imap.ThreadAlgorithm
	// A search's bare terms reached only the headers.
	headersOnly bool
	snap        *imap.StatusData
	fetched     time.Time
	lastUse     time.Time
	// Known wrong, as against merely aged.
	changed bool
}

type listingKey struct{ user, view string }

func newListingCache(keepFor time.Duration, sidebars *alborz.Memo[sidebar], bodies *bodyCache) *listingCache {
	return &listingCache{
		keepFor: keepFor, accountSidebars: sidebars, bodies: bodies,
		entries: make(map[listingKey]*listingEntry), refreshing: make(map[listingKey]bool),
		flights: make(map[listingKey]*listingFlight), generation: make(map[string]uint64),
	}
}

type listingCache struct {
	keepFor         time.Duration
	accountSidebars *alborz.Memo[sidebar]
	bodies          *bodyCache

	mu         sync.Mutex
	entries    map[listingKey]*listingEntry
	refreshing map[listingKey]bool
	flights    map[listingKey]*listingFlight
	generation map[string]uint64
}

type listingFlight struct {
	done     chan struct{}
	entry    *listingEntry
	err      error
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

// A reader never waits behind a speculative fetch not yet on the connection.
func (lc *listingCache) load(ctx context.Context, user, view string, perPage int, speculative bool, fetch func(context.Context) (*listingEntry, error)) (*listingEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !lc.keeps() {
		return fetch(ctx)
	}
	lc.mu.Lock()
	generation := lc.generation[user]
	k := listingKey{user, fmt.Sprintf("%s%ssize=%d&generation=%d", view, listingSep, perPage, generation)}
	f := lc.flights[k]
	if f == nil {
		f = &listingFlight{done: make(chan struct{}), queued: speculative}
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

func (lc *listingCache) keeps() bool {
	return lc.keepFor > 0
}

func (lc *listingCache) epoch(user string) uint64 {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.generation[user]
}

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

// listingSpec refetches a view without the request that first asked for it.
type listingSpec struct {
	mbox, query, view, sortKey, sortDir string
	thread                              imap.UID
}

func (spec listingSpec) reverse() bool {
	if spec.sortDir != "" {
		return spec.sortDir == "desc"
	}
	return sortKeys[spec.sortKey].descends
}

func (e *listingEntry) absorb(other *listingEntry) {
	e.msgs = append(e.msgs, other.msgs...)
	e.total += other.total
	e.headersOnly = e.headersOnly || other.headersOnly
	e.sortSupported = e.sortSupported && other.sortSupported
}

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

func (lc *listingCache) pageSize(user, view string, fallback int) int {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if e, ok := lc.entries[listingKey{user, view}]; ok {
		return e.perPage
	}
	return fallback
}

// A changed entry is read again at the size the page needs.
func (lc *listingCache) heldSize(user, view string) int {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if e, ok := lc.entries[listingKey{user, view}]; ok && !e.changed {
		return e.perPage
	}
	return 0
}

// A merge window holds rows only by what the merge compares.
func (lc *listingCache) fill(user, view string, rows []IMAPMessage) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	e := lc.entries[listingKey{user, view}]
	if e == nil {
		return
	}
	whole := make(map[imap.UID]IMAPMessage, len(rows))
	for _, r := range rows {
		whole[r.UID] = r
	}
	for i, m := range e.msgs {
		if r, ok := whole[m.UID]; ok && m.BodyStructure == nil {
			e.msgs[i] = r
		}
	}
}

func (lc *listingCache) storeAt(user, view string, e *listingEntry, generation uint64) {
	if !lc.keeps() {
		return
	}
	kept := e.snapshot()
	now := time.Now()
	kept.fetched, kept.lastUse, kept.changed = now, now, false
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.generation[user] != generation {
		return
	}
	if kept.sb.mailboxes != nil {
		lc.accountSidebars.Put(user, kept.sb.clone())
	}
	if kept.snap != nil {
		lc.bodies.observe(user, kept.snap.Mailbox, kept.snap.UIDValidity)
	}
	lc.entries[listingKey{user, view}] = kept
	maps.DeleteFunc(lc.entries, func(_ listingKey, old *listingEntry) bool { return time.Since(old.lastUse) > lc.keepFor })
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

func (lc *listingCache) markSeen(user, folder string, uid imap.UID) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
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
			e.reflag(m, folder, append(append([]imap.Flag(nil), m.Flags...), imap.FlagSeen))
			moved = true
		}
	}
	if moved {
		lc.railUnseen(user, folder, -1)
	}
}

func (lc *listingCache) railUnseen(user, folder string, delta int) {
	lc.accountSidebars.Update(user, func(sb sidebar) sidebar {
		sb = sb.clone()
		sb.adjustUnseen(folder, delta)
		return sb
	})
}

// An expunge renumbers sequence numbers, so a stale page is left to renew.
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
			if d := e.reflag(m, folder, append([]imap.Flag(nil), flags...)); d != 0 {
				moved = d
			}
			lc.bodies.flags(user, folder, []imap.UID{m.UID}, imap.StoreFlagsSet, flags)
		}
	}
	if moved != 0 {
		lc.railUnseen(user, folder, moved)
	}
}

// Filtered views are dropped: their membership may have changed.
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
			if d := e.reflag(m, folder, changedFlags(m.Flags, op, flags)); d != 0 {
				deltas[m.UID] = d
			}
		}
		e.fetched = time.Time{}
	}
	for _, delta := range deltas {
		lc.railUnseen(user, folder, delta)
	}
	// Messages no cached page shows moved the unseen count by an unknown amount.
	if len(shown) < len(uids) && (op == imap.StoreFlagsSet || slices.Contains(flags, imap.FlagSeen)) {
		lc.accountSidebars.Stale(user)
	}
	lc.bodies.flags(user, folder, uids, op, flags)
}

// A whole view can be a hundred thousand UIDs, walked under the caches' locks.
func uidSet(uids []imap.UID) map[imap.UID]bool {
	set := make(map[imap.UID]bool, len(uids))
	for _, uid := range uids {
		set[uid] = true
	}
	return set
}

// The buffer is copied: pages being rendered share it.
func (e *listingEntry) reflag(m *IMAPMessage, folder string, flags []imap.Flag) int {
	was := m.HasFlag(imap.FlagSeen)
	buf := *m.FetchMessageBuffer
	buf.Flags = flags
	m.FetchMessageBuffer = &buf
	now := m.HasFlag(imap.FlagSeen)
	if now == was {
		return 0
	}
	delta := 1
	if now {
		delta = -1
	}
	e.sb.adjustUnseen(folder, delta)
	return delta
}

func (e *listingEntry) row(uid imap.UID) *IMAPMessage {
	for i := range e.msgs {
		if e.msgs[i].UID == uid {
			return &e.msgs[i]
		}
	}
	return nil
}

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

// An arrival moves UIDNEXT, an expunge the total.
func (e *listingEntry) adjoins(other *listingEntry) bool {
	return e.snap != nil && other.snap != nil && e.total == other.total &&
		e.snap.UIDValidity == other.snap.UIDValidity && e.snap.UIDNext == other.snap.UIDNext
}

func (lc *listingCache) claim(user, view string) bool {
	if !lc.keeps() {
		return false
	}
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
		if k.user == user && shows(k.view, folder) {
			e.fetched, e.changed = time.Time{}, true
		}
	}
}

func (p *plugin) revalidate(s *alborz.Login, view string, e *listingEntry, folder func(*imapclient.Client) (string, error), fetch func(*imapclient.Client) (*listingEntry, error)) {
	user := s.Username()
	if !p.listings.claim(user, view) {
		return
	}
	generation := p.listings.epoch(user)
	go func() {
		defer p.listings.release(user, view)
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
				p.listings.refresh(user, view)
				return nil
			}
			fresh, err = fetch(c)
			return err
		})
		if err == nil && fresh != nil && fresh.snap != nil {
			p.listings.storeAt(user, view, fresh, generation)
		}
	}()
}

func (lc *listingCache) refresh(user, view string) {
	lc.mu.Lock()
	if e, ok := lc.entries[listingKey{user, view}]; ok {
		e.fetched = time.Now()
	}
	lc.mu.Unlock()
}

// Merged views' role names hide which folders they map to.
func shows(view, folder string) bool {
	return view == folder || strings.HasPrefix(view, folder+listingSep) || strings.HasPrefix(view, "#")
}

func (lc *listingCache) evict(user, folder string) {
	lc.drop(user, func(view string) bool { return shows(view, folder) })
}

func (lc *listingCache) evictAll(user string) {
	lc.drop(user, func(string) bool { return true })
}

func (lc *listingCache) drop(user string, match func(view string) bool) {
	lc.mu.Lock()
	lc.generation[user]++
	maps.DeleteFunc(lc.entries, func(k listingKey, _ *listingEntry) bool { return k.user == user && match(k.view) })
	lc.mu.Unlock()
	lc.accountSidebars.Stale(user)
}

// HIGHESTMODSEQ also moves on flag changes.
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
