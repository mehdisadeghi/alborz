package web

import (
	"testing"
	"time"

	"git.mehdix.org/alborz"
)

func TestNotificationPreviewsFollowTheRecipientSessionLock(t *testing.T) {
	s, _ := newSession()
	now := time.Now()
	if got := pushBody(s, "account", "preview", now); got != "account\npreview" {
		t.Fatalf("unprotected session: %q", got)
	}
	s.addPasskey(alborz.Passkey{Name: "test"})
	s.unlock(now)
	if got := pushBody(s, "account", "preview", now); got != "account\npreview" {
		t.Fatalf("unlocked passkey session: %q", got)
	}
	s.LockNow()
	if got := pushBody(s, "account", "preview", now); got != "account" {
		t.Fatalf("locked session leaked preview: %q", got)
	}
	s.unlock(now)
	if got := pushBody(s, "account", "preview", now.Add(s.Lock().Wait()+time.Second)); got != "account" {
		t.Fatalf("idle session leaked preview: %q", got)
	}
	if got := pushBody(nil, "account", "preview", now); got != "account" {
		t.Fatalf("unrestored session leaked preview: %q", got)
	}
}
