package alborz

import (
	"github.com/fernet/fernet-go"
	"mime/multipart"
	"testing"
	"time"
)

func TestExpiryUnlistsTheSession(t *testing.T) {
	sm := newSessionManager(nil, nil, nil, nil, nil, nil)
	s := &Session{manager: sm, closed: make(chan struct{}), pings: make(chan struct{}, 5),
		username: "a@test.local", token: "t"}
	sm.sessions[s.token] = s
	done := make(chan struct{})
	go func() {
		sm.reap(s)
		close(done)
	}()

	s.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the reaper did not finish")
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

// TestEnvelopeOpensAfterEitherKeyChanges seals under one login key and
// password, then opens with each changed in turn - and expects the
// wrap that no longer opened to have been renewed, so that the next
// change on the other side still opens. Both changed at once must
// refuse rather than guess.
func TestEnvelopeOpensAfterEitherKeyChanges(t *testing.T) {
	newKey := func() *fernet.Key {
		var k fernet.Key
		if err := k.Generate(); err != nil {
			t.Fatal(err)
		}
		return &k
	}
	session := func(key *fernet.Key, password string) *Session {
		return &Session{manager: &SessionManager{loginKey: key}, password: password}
	}
	key1, key2 := newKey(), newKey()
	e, err := session(key1, "first").Seal("app-password")
	if err != nil {
		t.Fatal(err)
	}

	plain, e, err := session(key1, "second").Open(e)
	if err != nil || plain != "app-password" {
		t.Fatalf("after a password change: %q, %v", plain, err)
	}
	plain, e, err = session(key2, "second").Open(e)
	if err != nil || plain != "app-password" {
		t.Fatalf("after a key rotation on the renewed password wrap: %q, %v", plain, err)
	}
	if _, _, err := session(newKey(), "third").Open(e); err != ErrSealed {
		t.Fatalf("both changed: %v, want ErrSealed", err)
	}
}
