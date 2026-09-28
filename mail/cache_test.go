package mail

import (
	"fmt"
	"testing"
	"time"

	"git.mehdix.org/alborz"
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
	// Whoever takes the snapshot has to ask for every field compared.
	partial := full()
	partial.UIDNext = 0
	if statusUnchanged(partial, full()) {
		t.Error("a snapshot missing UIDNEXT was accepted")
	}
}

func TestListingCacheStates(t *testing.T) {
	lc := newListingCache(listingKeepFor, alborz.NewBackgroundMemo[sidebar](listingFreshFor), newBodyCache(bodyKeepFor))
	lc.storeAt("u", "INBOX", &listingEntry{perPage: 25, total: 3}, lc.epoch("u"))
	lc.storeAt("u", listingView("INBOX", "hello", "", "", ""), &listingEntry{perPage: 25}, lc.epoch("u"))
	lc.storeAt("u", "Sent", &listingEntry{perPage: 25}, lc.epoch("u"))

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
	lc.entries[listingKey{"u", "Sent"}].lastUse = time.Now().Add(-2 * listingKeepFor)
	lc.storeAt("u", "Drafts", &listingEntry{perPage: 25}, lc.epoch("u"))
	if _, state := lc.lookup("u", "Sent", 25); state != listingMiss {
		t.Errorf("an entry unread past listingKeepFor was kept: %v", state)
	}
	if _, state := lc.lookup("u", "INBOX", 25); state == listingMiss {
		t.Errorf("a read entry was swept with it")
	}
	lc.storeAt("u", "Sent", &listingEntry{perPage: 25}, lc.epoch("u"))

	lc.evict("u", "INBOX")
	if _, state := lc.lookup("u", listingView("INBOX", "hello", "", "", ""), 25); state != listingMiss {
		t.Errorf("a search over the changed folder survived")
	}
	if _, state := lc.lookup("u", "Sent", 25); state != listingFresh {
		t.Errorf("another folder was dropped with it: %v", state)
	}
}

func TestListingCacheStaysBounded(t *testing.T) {
	lc := newListingCache(listingKeepFor, alborz.NewBackgroundMemo[sidebar](listingFreshFor), newBodyCache(bodyKeepFor))
	for i := 0; i < maxListingEntries+10; i++ {
		lc.storeAt("u", listingView("INBOX", fmt.Sprint("q", i), "", "", ""), &listingEntry{perPage: 25}, lc.epoch("u"))
	}
	if n := len(lc.entries); n != maxListingEntries {
		t.Errorf("a crawler minted %d entries; the cap is %d", n, maxListingEntries)
	}
}

func TestListingTakesFlagsInPlace(t *testing.T) {
	lc := newListingCache(listingKeepFor, alborz.NewBackgroundMemo[sidebar](listingFreshFor), newBodyCache(bodyKeepFor))
	unseen := uint32(2)
	status := &MailboxStatus{StatusData: &imap.StatusData{Mailbox: "INBOX", NumUnseen: &unseen}}
	sb := sidebar{statuses: map[string]*MailboxStatus{"INBOX": status}, active: status, inbox: status}
	row := func(uid imap.UID, seq uint32) IMAPMessage {
		return IMAPMessage{Mailbox: "INBOX", FetchMessageBuffer: &imapclient.FetchMessageBuffer{UID: uid, SeqNum: seq}}
	}
	lc.storeAt("u", "INBOX", &listingEntry{perPage: 25, sb: sb, msgs: []IMAPMessage{row(7, 1), row(8, 2)}}, lc.epoch("u"))

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

	// An expunge renumbers: seq 1 may name another message now.
	lc.stale("u", "INBOX")
	lc.setFlags("u", "INBOX", 1, []imap.Flag{imap.FlagFlagged})
	e, _ = lc.lookup("u", "INBOX", 25)
	if e.msgs[0].HasFlag(imap.FlagFlagged) {
		t.Errorf("a stale page took flags by sequence number: %v", e.msgs[0].Flags)
	}
}
