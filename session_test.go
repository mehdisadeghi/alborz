package alborz

import (
	"testing"
	"time"
)

func TestExpiryForgetsTheAccount(t *testing.T) {
	sm := newSessionManager(nil, nil, nil, nil, nil)
	gone := make(chan string, 1)
	sm.onGone = func(username string) { gone <- username }
	s := &Session{manager: sm, closed: make(chan struct{}), pings: make(chan struct{}, 5),
		username: "a@test.local", token: "t"}
	sm.sessions[s.token] = s
	go sm.reap(s)

	s.Close()
	select {
	case username := <-gone:
		if username != s.username {
			t.Errorf("forgot %q, not %q", username, s.username)
		}
	case <-time.After(time.Second):
		t.Fatal("the account was not forgotten")
	}
	if _, err := sm.get(s.token); err != ErrSessionExpired {
		t.Errorf("the session is still listed: %v", err)
	}
}
