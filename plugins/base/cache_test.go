package alborzbase

import (
	"fmt"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
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
	lc.entries[listingKey{"u", "INBOX"}].created = time.Now().Add(-2 * listingMaxAge)
	if _, state := lc.lookup("u", "INBOX", 25); state != listingMiss {
		t.Errorf("an entry past listingMaxAge was served: %v", state)
	}

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
