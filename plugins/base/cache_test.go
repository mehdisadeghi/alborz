package alborzbase

import (
	"fmt"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestStatusUnchangedNeedsEveryField(t *testing.T) {
	n := func(v uint32) *uint32 { return &v }
	full := func() *imap.StatusData {
		return &imap.StatusData{UIDValidity: 1, UIDNext: 10, NumMessages: n(9), NumUnseen: n(2), HighestModSeq: 5}
	}
	if !statusUnchanged(full(), full()) {
		t.Error("identical snapshots read as changed")
	}
	moved := full()
	moved.UIDNext = 11
	if statusUnchanged(full(), moved) {
		t.Error("a new message arriving went unnoticed")
	}
	// A snapshot taken without UIDNEXT can never match a live one, so
	// whoever takes the snapshot has to ask for every field compared.
	partial := full()
	partial.UIDNext = 0
	if statusUnchanged(partial, full()) {
		t.Error("a snapshot missing UIDNEXT was accepted")
	}
}

func TestListingCacheStates(t *testing.T) {
	lc := &listingCache{entries: make(map[listingKey]*listingEntry)}
	lc.store("u", "INBOX", &listingEntry{perPage: 25, total: 3})
	lc.store("u", listingView("INBOX", "hello", false, "", ""), &listingEntry{perPage: 25})
	lc.store("u", "Sent", &listingEntry{perPage: 25})

	if e, state := lc.lookup("u", "INBOX", 25); state != listingFresh || e.total != 3 {
		t.Fatalf("a just-stored entry is not fresh: %v", state)
	}
	if _, state := lc.lookup("u", "INBOX", 50); state != listingMiss {
		t.Errorf("a page of another size was served: %v", state)
	}
	lc.entries[listingKey{"u", "INBOX"}].fetched = time.Now().Add(-2 * listingFreshFor)
	if _, state := lc.lookup("u", "INBOX", 25); state != listingStale {
		t.Errorf("an entry past listingFreshFor is not stale: %v", state)
	}
	lc.refresh("u", "INBOX")
	if _, state := lc.lookup("u", "INBOX", 25); state != listingFresh {
		t.Errorf("a refreshed entry is not fresh again: %v", state)
	}
	// An entry nobody reads is let go of at the next store; one just
	// read is not, however old what it holds.
	lc.entries[listingKey{"u", "Sent"}].lastUse = time.Now().Add(-2 * listingKeepFor)
	lc.store("u", "Drafts", &listingEntry{perPage: 25})
	if _, state := lc.lookup("u", "Sent", 25); state != listingMiss {
		t.Errorf("an entry unread past listingKeepFor was kept: %v", state)
	}
	if _, state := lc.lookup("u", "INBOX", 25); state == listingMiss {
		t.Errorf("a read entry was swept with it")
	}
	lc.store("u", "Sent", &listingEntry{perPage: 25})

	// A change to one folder drops its searches with it and leaves the
	// other folders alone.
	lc.evict("u", "INBOX")
	if _, state := lc.lookup("u", listingView("INBOX", "hello", false, "", ""), 25); state != listingMiss {
		t.Errorf("a search over the changed folder survived")
	}
	if _, state := lc.lookup("u", "Sent", 25); state != listingFresh {
		t.Errorf("another folder was dropped with it: %v", state)
	}
}

func TestListingCacheStaysBounded(t *testing.T) {
	lc := &listingCache{entries: make(map[listingKey]*listingEntry)}
	for i := 0; i < maxListingEntries+10; i++ {
		lc.store("u", listingView("INBOX", fmt.Sprint("q", i), false, "", ""), &listingEntry{perPage: 25})
	}
	if n := len(lc.entries); n != maxListingEntries {
		t.Errorf("a crawler minted %d entries; the cap is %d", n, maxListingEntries)
	}
}

// TestListingTakesFlagsInPlace checks that a message read here, and a
// flag the server announced, change the cached row and the folder's
// unseen count without the listing being fetched again.
func TestListingTakesFlagsInPlace(t *testing.T) {
	lc := &listingCache{entries: make(map[listingKey]*listingEntry), refreshing: make(map[listingKey]bool)}
	unseen := uint32(2)
	status := &MailboxStatus{StatusData: &imap.StatusData{Mailbox: "INBOX", NumUnseen: &unseen}}
	sb := sidebar{statuses: map[string]*MailboxStatus{"INBOX": status}, active: status, inbox: status}
	row := func(uid imap.UID, seq uint32) IMAPMessage {
		return IMAPMessage{Mailbox: "INBOX", FetchMessageBuffer: &imapclient.FetchMessageBuffer{UID: uid, SeqNum: seq}}
	}
	lc.store("u", "INBOX", &listingEntry{perPage: 25, sb: sb, msgs: []IMAPMessage{row(7, 1), row(8, 2)}})

	lc.markSeen("u", "INBOX", 7)
	e, _ := lc.lookup("u", "INBOX", 25)
	if !e.msgs[0].HasFlag(imap.FlagSeen) || e.msgs[1].HasFlag(imap.FlagSeen) {
		t.Errorf("reading uid 7 marked the wrong row: %v %v", e.msgs[0].Flags, e.msgs[1].Flags)
	}
	if n := *e.sb.statuses["INBOX"].NumUnseen; n != 1 {
		t.Errorf("unseen after one read: %d", n)
	}

	lc.setFlags("u", "INBOX", 2, []imap.Flag{imap.FlagSeen, imap.FlagFlagged})
	e, _ = lc.lookup("u", "INBOX", 25)
	if !e.msgs[1].HasFlag(imap.FlagFlagged) || !e.msgs[1].HasFlag(imap.FlagSeen) {
		t.Errorf("the announced flags did not land on seq 2: %v", e.msgs[1].Flags)
	}
	if n := *e.sb.active.NumUnseen; n != 0 {
		t.Errorf("unseen after the server read seq 2: %d", n)
	}
}
