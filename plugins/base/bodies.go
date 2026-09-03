package alborzbase

import (
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
	// bodyKeepFor lets go of what nobody has opened for this long, so
	// an account that never comes back does not hold its pages.
	bodyKeepFor = listingKeepFor
)

type bodyKey struct {
	user, mbox string
	uid        imap.UID
}

type cachedBody struct {
	buf  *imapclient.FetchMessageBuffer
	part []int
	// permanent is what the folder let the page offer as flags, read
	// from the connection that fetched the body.
	permanent []imap.Flag
	lastUse   time.Time
}

var bodies = &bodyCache{entries: make(map[bodyKey]*cachedBody)}

type bodyCache struct {
	mu      sync.Mutex
	entries map[bodyKey]*cachedBody
}

// get returns the held body when it is the part asked for.
func (bc *bodyCache) get(user, mbox string, uid imap.UID, part []int) *cachedBody {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	b, ok := bc.entries[bodyKey{user, mbox, uid}]
	if !ok || !samePath(b.part, part) {
		return nil
	}
	b.lastUse = time.Now()
	return b
}

func (bc *bodyCache) has(user, mbox string, uid imap.UID) bool {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	_, ok := bc.entries[bodyKey{user, mbox, uid}]
	return ok
}

func (bc *bodyCache) put(user, mbox string, b *cachedBody) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	b.lastUse = time.Now()
	bc.entries[bodyKey{user, mbox, b.buf.UID}] = b
	held := 0
	for k, e := range bc.entries {
		if time.Since(e.lastUse) > bodyKeepFor {
			delete(bc.entries, k)
			continue
		}
		if k.user == user {
			held++
		}
	}
	// The least recently read go until the account is within bounds.
	for ; held > bodyKeepPerUser; held-- {
		var oldest bodyKey
		var when time.Time
		for k, e := range bc.entries {
			if k.user == user && (when.IsZero() || e.lastUse.Before(when)) {
				oldest, when = k, e.lastUse
			}
		}
		delete(bc.entries, oldest)
	}
}

func (bc *bodyCache) forget(user string) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	for k := range bc.entries {
		if k.user == user {
			delete(bc.entries, k)
		}
	}
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
func prefetchBodies(s *alborz.Session, preferHTML bool, mbox string, rows []IMAPMessage) error {
	user := s.Username()
	byPath := map[string][]imap.UID{}
	paths := map[string][]int{}
	for i := range rows {
		row := &rows[i]
		if bodies.has(user, mbox, row.UID) {
			continue
		}
		part := row.PreferredPart(preferHTML)
		if part == nil || len(part.Path) == 0 || part.Size > bodyMaxPartSize {
			continue
		}
		key := part.PathString()
		byPath[key] = append(byPath[key], row.UID)
		paths[key] = part.Path
	}
	for key, uids := range byPath {
		for len(uids) > 0 {
			n := min(bodyFetchBatch, len(uids))
			batch, path := uids[:n], paths[key]
			uids = uids[n:]
			err := s.DoIMAP(func(c *imapclient.Client) error {
				if err := ensureMailboxSelected(c, mbox); err != nil {
					return err
				}
				msgs, err := c.Fetch(imap.UIDSetNum(batch...), partFetchOptions(path, true)).Collect()
				if err != nil {
					return err
				}
				permanent := c.Mailbox().PermanentFlags
				for _, msg := range msgs {
					bodies.put(user, mbox, &cachedBody{buf: msg, part: path, permanent: permanent})
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
