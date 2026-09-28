package alborz

import (
	"testing"
	"time"
)

func TestASessionLocksOnlyWithAPasskeyAndOnlyAfterTimeAway(t *testing.T) {
	s, _ := newSession()
	now := time.Now()
	if s.Locked(now.Add(365 * 24 * time.Hour)) {
		t.Fatal("a session with no passkey locked")
	}

	s.addPasskey(Passkey{Name: "key"})
	s.SetLockAfter(10 * time.Minute)
	s.act(now)
	if s.Locked(now.Add(9 * time.Minute)) {
		t.Fatal("locked inside its lock time")
	}
	if !s.Locked(now.Add(11 * time.Minute)) {
		t.Fatal("still open past its lock time")
	}
	s.act(now.Add(12 * time.Minute))
	if !s.Locked(now.Add(12 * time.Minute)) {
		t.Fatal("a request unlocked a locked session")
	}
	s.unlock(now.Add(13 * time.Minute))
	if s.Locked(now.Add(14 * time.Minute)) {
		t.Fatal("locked right after unlocking")
	}

	if back := sessionFromRecord(s.record()); !back.Locked(time.Now()) {
		t.Fatal("a session with a passkey came back from its record unlocked")
	}
}

func TestALockedSessionAnswersOnlyTheWayInAndOut(t *testing.T) {
	for path, open := range map[string]bool{
		"/unlock": true, "/unlock/finish": true, "/unlock/leave": true, "/assets/style.css": true, "/login": true,
		"/mailbox/INBOX": false, "/events": false, "/logout": false, "/settings/lock": false, "/unlocked": false,
	} {
		if lockOpen(path) != open {
			t.Errorf("%s: open=%v, want %v", path, !open, open)
		}
	}
}
