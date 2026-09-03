package alborzbase

import (
	"testing"

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
