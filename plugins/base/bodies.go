package alborzbase

import (
	"slices"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// The body cache holds, for each row of a listing, the part the message
// page would open, fetched behind the page so that opening a message
// asks the server nothing. A body never changes; what is held goes
// when the account does, or when newer pages push it out.
const (
	// bodyKeepPerUser bounds what one account holds: two pages' worth.
	bodyKeepPerUser = 100
	// bodyMaxPartSize is the largest part fetched ahead of a click that
	// may never come; a larger one is fetched when opened, as before.
	bodyMaxPartSize = 512 << 10
	// bodyFetchBatch is how many messages one FETCH asks for. A click
	// arriving behind the batch waits for that one round trip.
	bodyFetchBatch = 10
	// A shared byte limit keeps many idle accounts from exhausting memory.
	bodyMaxBytes = 64 << 20
	// bodyKeepFor lets go of what nobody has opened for this long, so
	// an account that never comes back does not hold its pages.
	bodyKeepFor = listingKeepFor
)

type bodyKey struct {
	user, mbox string
	validity   uint32
	uid        imap.UID
}

type cachedBody struct {
	validity uint32
	buf      *imapclient.FetchMessageBuffer
	part     []int
	// permanent is what the folder let the page offer as flags, read
	// from the connection that fetched the body.
	permanent []imap.Flag
	lastUse   time.Time
}

var bodies = newBodyCache()

type mailboxKey struct{ user, mailbox string }

func newBodyCache() *bodyCache {
	return &bodyCache{entries: make(map[bodyKey]*cachedBody), fetching: make(map[bodyKey]uint64),
		validity: make(map[mailboxKey]uint32), seen: make(map[bodyKey]uint64)}
}

type bodyCache struct {
	mu       sync.Mutex
	entries  map[bodyKey]*cachedBody
	fetching map[bodyKey]uint64
	next     uint64
	validity map[mailboxKey]uint32
	seen     map[bodyKey]uint64
}

// get returns the held body when it is the part asked for.
func (bc *bodyCache) get(user, mbox string, validity uint32, uid imap.UID, part []int) *cachedBody {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	b, ok := bc.entries[bodyKey{user, mbox, validity, uid}]
	if !ok || !samePath(b.part, part) {
		return nil
	}
	b.lastUse = time.Now()
	return b
}

func (bc *bodyCache) claim(k bodyKey, part []int) uint64 {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if b := bc.entries[k]; b != nil && samePath(b.part, part) {
		return 0
	}
	if bc.fetching[k] != 0 {
		return 0
	}
	bc.next++
	bc.fetching[k] = bc.next
	return bc.next
}

func (bc *bodyCache) put(user, mbox string, validity uint32, token uint64, b *cachedBody) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	k := bodyKey{user, mbox, validity, b.buf.UID}
	if token == 0 || bc.fetching[k] != token {
		return
	}
	if bc.validity[mailboxKey{user, mbox}] != validity {
		return
	}
	b.validity = validity
	b.lastUse = time.Now()
	bc.entries[k] = b
	held, bytes := 0, 0
	for k, e := range bc.entries {
		if time.Since(e.lastUse) > bodyKeepFor {
			delete(bc.entries, k)
			continue
		}
		if k.user == user {
			held++
		}
		bytes += e.size()
	}
	// The least recently read go until the account is within bounds.
	for held > bodyKeepPerUser || bytes > bodyMaxBytes {
		var oldest bodyKey
		var when time.Time
		for k, e := range bc.entries {
			if (held <= bodyKeepPerUser || k.user == user) && (when.IsZero() || e.lastUse.Before(when)) {
				oldest, when = k, e.lastUse
			}
		}
		bytes -= bc.entries[oldest].size()
		if oldest.user == user {
			held--
		}
		delete(bc.entries, oldest)
	}
}

// discard removes messages after a move or deletion, including any in-flight
// fetch or automatic-read token. A nil UID list removes the whole mailbox.
func (bc *bodyCache) discard(user, mailbox string, uids []imap.UID) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	matches := func(k bodyKey) bool {
		return k.user == user && k.mbox == mailbox && (uids == nil || slices.Contains(uids, k.uid))
	}
	for k := range bc.entries {
		if matches(k) {
			delete(bc.entries, k)
		}
	}
	for k := range bc.fetching {
		if matches(k) {
			delete(bc.fetching, k)
		}
	}
	for k := range bc.seen {
		if matches(k) {
			delete(bc.seen, k)
		}
	}
}

func (bc *bodyCache) forget(user string) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	for k := range bc.validity {
		if k.user == user {
			delete(bc.validity, k)
		}
	}
	for k := range bc.seen {
		if k.user == user {
			delete(bc.seen, k)
		}
	}
	for k := range bc.fetching {
		if k.user == user {
			delete(bc.fetching, k)
		}
	}
	for k := range bc.entries {
		if k.user == user {
			delete(bc.entries, k)
		}
	}
}

// Flag changes retain body bytes and cancel prefetches taken before the write.
func (bc *bodyCache) flags(user, mbox string, uids []imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	for k := range bc.fetching {
		if k.user == user && k.mbox == mbox && slices.Contains(uids, k.uid) {
			delete(bc.fetching, k)
		}
	}
	for k, b := range bc.entries {
		if k.user != user || k.mbox != mbox || !slices.Contains(uids, k.uid) {
			continue
		}
		updated := *b
		buf := *b.buf
		buf.Flags = changedFlags(buf.Flags, op, flags)
		updated.buf = &buf
		bc.entries[k] = &updated
	}
}

func (b *cachedBody) size() int {
	n := 0
	for _, part := range b.buf.BodySection {
		n += len(part.Bytes)
	}
	return n
}

func samePath(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// prefetchBodies fetches the preferred part of each row not held yet,
// a batch of messages per FETCH and a FETCH per part path, off the
// reader's time. The fetch peeks, so nothing is marked read by it.
func prefetchBodies(s *alborz.Session, preferHTML bool, mbox string, validity uint32, rows []IMAPMessage) error {
	if validity == 0 {
		return nil
	}
	user := s.Username()
	claimed := make(map[bodyKey]uint64)
	defer func() {
		bodies.mu.Lock()
		for k, token := range claimed {
			if bodies.fetching[k] == token {
				delete(bodies.fetching, k)
			}
		}
		bodies.mu.Unlock()
	}()
	byPath := map[string][]imap.UID{}
	paths := map[string][]int{}
	for i := range rows {
		row := &rows[i]
		// A signed message is verified from its whole raw bytes on the
		// connection, so the page fetches it itself.
		if _, _, ok := signedParts(row.BodyStructure); ok {
			continue
		}
		part := row.PreferredPart(preferHTML)
		if part == nil || len(part.Path) == 0 || part.Size > bodyMaxPartSize {
			continue
		}
		key := part.PathString()
		body := bodyKey{user, mbox, validity, row.UID}
		token := bodies.claim(body, part.Path)
		if token == 0 {
			continue
		}
		claimed[body] = token
		byPath[key] = append(byPath[key], row.UID)
		paths[key] = part.Path
	}
	var lastClient *imapclient.Client
	var lastSelection *imapclient.SelectedMailbox
	for key, uids := range byPath {
		for len(uids) > 0 {
			n := min(bodyFetchBatch, len(uids))
			batch, path := uids[:n], paths[key]
			uids = uids[n:]
			err := s.DoIMAPBackground(func(c *imapclient.Client) error {
				bodies.mu.Lock()
				active := make([]imap.UID, 0, len(batch))
				for _, uid := range batch {
					k := bodyKey{user, mbox, validity, uid}
					if bodies.fetching[k] == claimed[k] {
						active = append(active, uid)
					}
				}
				bodies.mu.Unlock()
				if len(active) == 0 {
					return nil
				}
				if c != lastClient || c.Mailbox() != lastSelection || lastSelection == nil {
					selected, err := c.Select(mbox, nil).Wait()
					if err != nil {
						return err
					}
					mailboxIdentified(user, mbox, selected.UIDValidity)
					if selected.UIDValidity != validity {
						return nil
					}
					lastClient, lastSelection = c, c.Mailbox()
				}
				msgs, err := c.Fetch(imap.UIDSetNum(active...), partFetchOptions(path, true)).Collect()
				if err != nil {
					return err
				}
				permanent := c.Mailbox().PermanentFlags
				for _, msg := range msgs {
					bodies.put(user, mbox, validity, claimed[bodyKey{user, mbox, validity, msg.UID}], &cachedBody{buf: msg, part: path, permanent: permanent})
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func changedFlags(current []imap.Flag, op imap.StoreFlagsOp, flags []imap.Flag) []imap.Flag {
	current = append([]imap.Flag(nil), current...)
	switch op {
	case imap.StoreFlagsSet:
		current = append([]imap.Flag(nil), flags...)
	case imap.StoreFlagsAdd:
		for _, f := range flags {
			if !slices.Contains(current, f) {
				current = append(current, f)
			}
		}
	case imap.StoreFlagsDel:
		current = slices.DeleteFunc(current, func(f imap.Flag) bool { return slices.Contains(flags, f) })
	}
	return current
}

// Mailbox identity outlives any one cached page. Resetting UIDVALIDITY
// invalidates bodies and pending writes, not just listing membership.
func (bc *bodyCache) observe(user, mailbox string, validity uint32) {
	if validity == 0 {
		return
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()
	key := mailboxKey{user, mailbox}
	if bc.validity[key] == validity {
		return
	}
	bc.validity[key] = validity
	for k := range bc.entries {
		if k.user == user && k.mbox == mailbox && k.validity != validity {
			delete(bc.entries, k)
		}
	}
	for k := range bc.fetching {
		if k.user == user && k.mbox == mailbox && k.validity != validity {
			delete(bc.fetching, k)
		}
	}
	for k := range bc.seen {
		if k.user == user && k.mbox == mailbox && k.validity != validity {
			delete(bc.seen, k)
		}
	}
}

func (bc *bodyCache) current(user, mailbox string, uid imap.UID, part []int) *cachedBody {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	b := bc.entries[bodyKey{user, mailbox, bc.validity[mailboxKey{user, mailbox}], uid}]
	if b == nil || (len(part) != 0 && !samePath(b.part, part)) {
		return nil
	}
	b.lastUse = time.Now()
	return b
}

func (bc *bodyCache) cancelSeen(user, mailbox string, uids []imap.UID) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	for k := range bc.seen {
		if k.user == user && k.mbox == mailbox && slices.Contains(uids, k.uid) {
			delete(bc.seen, k)
		}
	}
}

func markHeldSeen(s *alborz.Session, mailbox string, uid imap.UID, validity uint32) {
	k := bodyKey{s.Username(), mailbox, validity, uid}
	bodies.mu.Lock()
	if bodies.seen[k] != 0 {
		bodies.mu.Unlock()
		return
	}
	bodies.next++
	token := bodies.next
	bodies.seen[k] = token
	bodies.mu.Unlock()
	go func() {
		defer func() {
			bodies.mu.Lock()
			if bodies.seen[k] == token {
				delete(bodies.seen, k)
			}
			bodies.mu.Unlock()
		}()
		s.DoIMAP(func(c *imapclient.Client) error {
			bodies.mu.Lock()
			current := bodies.seen[k] == token
			bodies.mu.Unlock()
			if !current {
				return nil
			}
			if err := storeFlags(c, mailbox, imap.UIDSetNum(uid), imap.StoreFlagsAdd, []imap.Flag{imap.FlagSeen}); err != nil {
				return err
			}
			// Keep confirmation on the same connection turn as the STORE.
			messageRead(s.Username(), mailbox, uid)
			return nil
		})
	}()
}
