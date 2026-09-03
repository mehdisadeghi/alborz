package alborz

import (
	"mime/multipart"
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

func TestFullAttachmentCacheStaysUsable(t *testing.T) {
	s := &Session{attachments: make(map[string]*Attachment)}
	big := &multipart.FileHeader{Size: MaxAttachmentSize}
	if _, err := s.PutAttachment(big, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAttachment(big, nil); err != ErrAttachmentCacheSize {
		t.Fatalf("a second full-size attachment was taken: %v", err)
	}
	done := make(chan struct{})
	go func() {
		s.PopAttachment("none")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the cache stayed locked after refusing an attachment")
	}
}
