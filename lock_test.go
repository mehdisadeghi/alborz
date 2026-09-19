package alborz

import (
	"testing"
	"time"
)

func TestAVisitLocksOnlyWithAPasskeyAndOnlyAfterTimeAway(t *testing.T) {
	v, _ := newVisit()
	now := time.Now()
	if v.Locked(now.Add(365 * 24 * time.Hour)) {
		t.Fatal("a visit with no passkey locked")
	}

	v.addPasskey(Passkey{Name: "key"})
	v.SetLockAfter(10 * time.Minute)
	v.act(now)
	if v.Locked(now.Add(9 * time.Minute)) {
		t.Fatal("locked inside its lock time")
	}
	if !v.Locked(now.Add(11 * time.Minute)) {
		t.Fatal("still open past its lock time")
	}
	// Coming back does not open it: only the passkey does.
	v.act(now.Add(12 * time.Minute))
	if !v.Locked(now.Add(12 * time.Minute)) {
		t.Fatal("a request unlocked a locked visit")
	}
	v.unlock(now.Add(13 * time.Minute))
	if v.Locked(now.Add(14 * time.Minute)) {
		t.Fatal("locked right after unlocking")
	}

	// What a restart rebuilds has nobody at it yet.
	if back := visitFromRecord(v.record()); !back.Locked(time.Now()) {
		t.Fatal("a visit with a passkey came back from its record unlocked")
	}
}

func TestALockedVisitAnswersOnlyTheWayInAndOut(t *testing.T) {
	for path, open := range map[string]bool{
		"/unlock": true, "/unlock/finish": true, "/unlock/leave": true, "/assets/style.css": true, "/login": true,
		"/mailbox/INBOX": false, "/events": false, "/logout": false, "/settings/lock": false, "/unlocked": false,
	} {
		if lockOpen(path) != open {
			t.Errorf("%s: open=%v, want %v", path, !open, open)
		}
	}
}
