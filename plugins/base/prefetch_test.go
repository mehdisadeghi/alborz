package alborzbase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestBodyIdentityAndFlagWrite(t *testing.T) {
	bc := newBodyCache()
	k := bodyKey{user: "u", mbox: "INBOX", validity: 1, uid: 7}
	part := []int{1}
	bc.observe(k.user, k.mbox, k.validity)
	token := bc.claim(k, part)
	bc.put(k.user, k.mbox, k.validity, token, &cachedBody{buf: &imapclient.FetchMessageBuffer{UID: k.uid}, part: part})
	if bc.get(k.user, k.mbox, 2, k.uid, part) != nil {
		t.Fatal("body survived a UIDVALIDITY change")
	}
	bc.flags(k.user, k.mbox, []imap.UID{k.uid}, imap.StoreFlagsAdd, []imap.Flag{imap.FlagFlagged})
	bc.put(k.user, k.mbox, k.validity, token, &cachedBody{buf: &imapclient.FetchMessageBuffer{UID: k.uid}, part: part})
	b := bc.get(k.user, k.mbox, k.validity, k.uid, part)
	if b == nil || len(b.buf.Flags) != 1 || b.buf.Flags[0] != imap.FlagFlagged {
		t.Fatal("prefetch overwrote confirmed flags")
	}
}

func TestListingFetchOutlivesItsFirstReader(t *testing.T) {
	lc := newListingCache()
	started, finish := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := lc.load(ctx, "u", "INBOX", 25, false, func(work context.Context) (*listingEntry, error) {
			close(started)
			<-finish
			if err := work.Err(); err != nil {
				return nil, err
			}
			return &listingEntry{perPage: 25, total: 7}, nil
		})
		first <- err
	}()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first reader: %v", err)
	}
	close(finish)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, err := lc.load(ctx, "u", "INBOX", 25, false, func(context.Context) (*listingEntry, error) {
		t.Error("a second fetch was started")
		return nil, errors.New("duplicate fetch")
	})
	if err != nil || e == nil || e.total != 7 {
		t.Fatalf("second reader: %v, %v", e, err)
	}
}

func TestMailboxResetInvalidatesBodiesAndPendingWrites(t *testing.T) {
	bc := newBodyCache()
	k := bodyKey{user: "u", mbox: "INBOX", validity: 1, uid: 7}
	bc.observe(k.user, k.mbox, k.validity)
	token := bc.claim(k, []int{1})
	bc.put(k.user, k.mbox, k.validity, token, &cachedBody{buf: &imapclient.FetchMessageBuffer{UID: k.uid}, part: []int{1}})
	bc.seen[k] = 1
	if bc.current(k.user, k.mbox, k.uid, nil) == nil {
		t.Fatal("body requires a listing")
	}
	bc.cancelSeen(k.user, k.mbox, []imap.UID{k.uid})
	if bc.seen[k] != 0 {
		t.Fatal("explicit write left an automatic read pending")
	}
	bc.seen[k] = 2
	bc.observe(k.user, k.mbox, 2)
	bc.put(k.user, k.mbox, k.validity, token, &cachedBody{buf: &imapclient.FetchMessageBuffer{UID: k.uid}, part: []int{1}})
	if bc.current(k.user, k.mbox, k.uid, nil) != nil || bc.seen[k] != 0 || bc.fetching[k] != 0 {
		t.Fatal("mailbox reset retained old identities")
	}
}

func TestListingFetchCannotUndoInvalidation(t *testing.T) {
	lc := newListingCache()
	_, err := lc.load(context.Background(), "u", "INBOX", 25, false, func(context.Context) (*listingEntry, error) {
		lc.evict("u", "INBOX")
		return &listingEntry{perPage: 25, total: 3}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := lc.lookup("u", "INBOX", 25); e != nil {
		t.Fatal("an invalidated fetch repopulated the cache")
	}
}

func TestRemovedBodyCannotReturnFromPrefetch(t *testing.T) {
	bc := newBodyCache()
	bc.observe("u", "INBOX", 1)
	for _, uid := range []imap.UID{7, 8} {
		k := bodyKey{"u", "INBOX", 1, uid}
		token := bc.claim(k, []int{1})
		bc.put("u", "INBOX", 1, token, &cachedBody{buf: &imapclient.FetchMessageBuffer{UID: uid}, part: []int{1}})
		bc.seen[k] = token
	}
	k := bodyKey{"u", "INBOX", 1, 7}
	token := bc.fetching[k]
	bc.discard("u", "INBOX", []imap.UID{7})
	bc.put("u", "INBOX", 1, token, &cachedBody{buf: &imapclient.FetchMessageBuffer{UID: 7}, part: []int{1}})
	if bc.current("u", "INBOX", 7, nil) != nil || bc.seen[k] != 0 {
		t.Fatal("removed message retained a body or pending read")
	}
	if bc.current("u", "INBOX", 8, nil) == nil {
		t.Fatal("removing one message discarded another")
	}
	bc.discard("u", "INBOX", nil)
	if len(bc.entries) != 0 || len(bc.fetching) != 0 || len(bc.seen) != 0 {
		t.Fatal("emptying the mailbox retained cached messages")
	}
}
