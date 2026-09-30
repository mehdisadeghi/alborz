package mail

import (
	"maps"
	"slices"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Bodies are fetched behind the listing so opening a message asks the server nothing.
const (
	bodyKeepPerUser = 100
	// Larger parts are not worth fetching for a click that may never come.
	bodyMaxPartSize = 512 << 10
	// Bounds the round trip a click can wait behind.
	bodyFetchBatch = 10
	// Keeps many idle accounts from exhausting memory.
	bodyMaxBytes = 64 << 20
	bodyKeepFor  = listingKeepFor
)

type bodyKey struct {
	user, mbox string
	validity   uint32
	uid        imap.UID
}

type cachedBody struct {
	validity  uint32
	buf       *imapclient.FetchMessageBuffer
	part      []int
	permanent []imap.Flag
	lastUse   time.Time
}

type mailboxKey struct{ user, mailbox string }

func newBodyCache(keepFor time.Duration) *bodyCache {
	return &bodyCache{keepFor: keepFor, entries: make(map[bodyKey]*cachedBody), fetching: make(map[bodyKey]uint64),
		validity: make(map[mailboxKey]uint32), seen: make(map[bodyKey]uint64)}
}

type bodyCache struct {
	keepFor  time.Duration
	mu       sync.Mutex
	entries  map[bodyKey]*cachedBody
	fetching map[bodyKey]uint64
	next     uint64
	validity map[mailboxKey]uint32
	seen     map[bodyKey]uint64
}

func (bc *bodyCache) claim(k bodyKey, part []int) uint64 {
	if bc.keepFor == 0 {
		return 0
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if b := bc.entries[k]; b != nil && slices.Equal(b.part, part) || bc.fetching[k] != 0 {
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
	if token == 0 || bc.fetching[k] != token || bc.validity[mailboxKey{user, mbox}] != validity {
		return
	}
	b.validity = validity
	b.lastUse = time.Now()
	bc.entries[k] = b
	held, bytes := 0, 0
	for k, e := range bc.entries {
		if time.Since(e.lastUse) > bc.keepFor {
			delete(bc.entries, k)
			continue
		}
		if k.user == user {
			held++
		}
		bytes += e.size()
	}
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

// The caller holds bc.mu.
func (bc *bodyCache) drop(match func(bodyKey) bool) {
	maps.DeleteFunc(bc.entries, func(k bodyKey, _ *cachedBody) bool { return match(k) })
	maps.DeleteFunc(bc.fetching, func(k bodyKey, _ uint64) bool { return match(k) })
	maps.DeleteFunc(bc.seen, func(k bodyKey, _ uint64) bool { return match(k) })
}

// Nil uids is the whole mailbox.
func (bc *bodyCache) discard(user, mailbox string, uids []imap.UID) {
	among := uidSet(uids)
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.drop(func(k bodyKey) bool { return k.user == user && k.mbox == mailbox && (uids == nil || among[k.uid]) })
}

func (bc *bodyCache) forget(user string) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	maps.DeleteFunc(bc.validity, func(k mailboxKey, _ uint32) bool { return k.user == user })
	bc.drop(func(k bodyKey) bool { return k.user == user })
}

// Prefetches taken before the write would carry the old flags.
func (bc *bodyCache) flags(user, mbox string, uids []imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) {
	among := uidSet(uids)
	bc.mu.Lock()
	defer bc.mu.Unlock()
	maps.DeleteFunc(bc.fetching, func(k bodyKey, _ uint64) bool { return k.user == user && k.mbox == mbox && among[k.uid] })
	for k, b := range bc.entries {
		if k.user != user || k.mbox != mbox || !among[k.uid] {
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

func (p *Mailboxes) prefetchBodies(s *alborz.Login, preferHTML bool, mbox string, validity uint32, rows []IMAPMessage) {
	if validity == 0 {
		return
	}
	user := s.Username()
	claimed := make(map[bodyKey]uint64)
	byPath := map[string][]imap.UID{}
	paths := map[string][]int{}
	for i := range rows {
		row := &rows[i]
		part := row.PreferredPart(preferHTML)
		if part == nil || len(part.Path) == 0 || part.Size > bodyMaxPartSize {
			continue
		}
		key := part.PathString()
		body := bodyKey{user, mbox, validity, row.UID}
		token := p.bodies.claim(body, part.Path)
		if token == 0 {
			continue
		}
		claimed[body] = token
		byPath[key] = append(byPath[key], row.UID)
		paths[key] = part.Path
	}
	if len(claimed) == 0 {
		return
	}
	go func() {
		defer func() {
			p.bodies.mu.Lock()
			maps.DeleteFunc(p.bodies.fetching, func(k bodyKey, token uint64) bool { return claimed[k] == token })
			p.bodies.mu.Unlock()
		}()
		var lastClient *imapclient.Client
		var lastSelection *imapclient.SelectedMailbox
		for key, uids := range byPath {
			for len(uids) > 0 {
				n := min(bodyFetchBatch, len(uids))
				batch, path := uids[:n], paths[key]
				uids = uids[n:]
				err := s.DoIMAPBackground(func(c *imapclient.Client) error {
					p.bodies.mu.Lock()
					active := make([]imap.UID, 0, len(batch))
					for _, uid := range batch {
						k := bodyKey{user, mbox, validity, uid}
						if p.bodies.fetching[k] == claimed[k] {
							active = append(active, uid)
						}
					}
					p.bodies.mu.Unlock()
					if len(active) == 0 {
						return nil
					}
					if c != lastClient || c.Mailbox() != lastSelection || lastSelection == nil {
						selected, err := c.Select(mbox, nil).Wait()
						if err != nil {
							return err
						}
						p.mailboxIdentified(user, mbox, selected.UIDValidity)
						if selected.UIDValidity != validity {
							return nil
						}
						lastClient, lastSelection = c, c.Mailbox()
					}
					msgs, err := c.Fetch(imap.UIDSetNum(active...), partFetchOptions(path, true, true)).Collect()
					if err != nil {
						return err
					}
					permanent := c.Mailbox().PermanentFlags
					for _, msg := range msgs {
						p.bodies.put(user, mbox, validity, claimed[bodyKey{user, mbox, validity, msg.UID}], &cachedBody{buf: msg, part: path, permanent: permanent})
					}
					return nil
				})
				if err != nil {
					return
				}
			}
		}
	}()
}

func changedFlags(current []imap.Flag, op imap.StoreFlagsOp, flags []imap.Flag) []imap.Flag {
	switch op {
	case imap.StoreFlagsSet:
		return append([]imap.Flag(nil), flags...)
	case imap.StoreFlagsDel:
		return slices.DeleteFunc(append([]imap.Flag(nil), current...), func(f imap.Flag) bool { return slices.Contains(flags, f) })
	}
	current = append([]imap.Flag(nil), current...)
	for _, f := range flags {
		if !slices.Contains(current, f) {
			current = append(current, f)
		}
	}
	return current
}

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
	bc.drop(func(k bodyKey) bool { return k.user == user && k.mbox == mailbox && k.validity != validity })
}

func (bc *bodyCache) current(user, mailbox string, uid imap.UID, part []int) *cachedBody {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	b := bc.entries[bodyKey{user, mailbox, bc.validity[mailboxKey{user, mailbox}], uid}]
	if b == nil || (len(part) != 0 && !slices.Equal(b.part, part)) {
		return nil
	}
	b.lastUse = time.Now()
	return b
}

func (bc *bodyCache) cancelSeen(user, mailbox string, uids []imap.UID) {
	among := uidSet(uids)
	bc.mu.Lock()
	defer bc.mu.Unlock()
	maps.DeleteFunc(bc.seen, func(k bodyKey, _ uint64) bool { return k.user == user && k.mbox == mailbox && among[k.uid] })
}

func (p *Mailboxes) markHeldSeen(s *alborz.Login, mailbox string, uid imap.UID, validity uint32) {
	k := bodyKey{s.Username(), mailbox, validity, uid}
	p.bodies.mu.Lock()
	if p.bodies.seen[k] != 0 {
		p.bodies.mu.Unlock()
		return
	}
	p.bodies.next++
	token := p.bodies.next
	p.bodies.seen[k] = token
	p.bodies.mu.Unlock()
	go func() {
		defer func() {
			p.bodies.mu.Lock()
			if p.bodies.seen[k] == token {
				delete(p.bodies.seen, k)
			}
			p.bodies.mu.Unlock()
		}()
		s.DoIMAP(func(c *imapclient.Client) error {
			p.bodies.mu.Lock()
			current := p.bodies.seen[k] == token
			p.bodies.mu.Unlock()
			if !current {
				return nil
			}
			if err := storeFlags(c, mailbox, imap.UIDSetNum(uid), imap.StoreFlagsAdd, []imap.Flag{imap.FlagSeen}); err != nil {
				return err
			}
			p.messageRead(s.Username(), mailbox, uid)
			return nil
		})
	}()
}
