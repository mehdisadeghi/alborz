package alborz

import (
	"errors"
	"mime/multipart"
	"testing"
	"time"
)

func TestExpiryUnlistsTheLogin(t *testing.T) {
	sm := newLoginManager(nil, nil, nil, nil, nil, nil)
	s := &Login{manager: sm, closed: make(chan struct{}), pings: make(chan struct{}, 5),
		username: "a@test.local"}
	sm.logins[s] = struct{}{}
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
	if _, listed := sm.logins[s]; listed {
		t.Error("the login is still listed")
	}
}

func TestFullAttachmentCacheStaysUsable(t *testing.T) {
	s, _ := newSession()
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

func TestEnvelopeOpensOnlyWithTheMailPassword(t *testing.T) {
	login := func(password string) *Login { return &Login{password: password} }
	e, err := login("first").Seal("app-password")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := login("first").Open(e); err != nil || plain != "app-password" {
		t.Fatalf("with the password it was sealed under: %q, %v", plain, err)
	}
	if _, err := login("second").Open(e); err != ErrSealed {
		t.Fatalf("after a password change: %v, want ErrSealed", err)
	}
}

func TestSideBySideTriesAreCountedBeforeTheyAreAnswered(t *testing.T) {
	r := newRefusals(signInTries, signInWindow)
	asked := make(chan struct{}, 4*signInTries)
	answer := make(chan struct{})
	results := make(chan error, 4*signInTries)
	for range 4 * signInTries {
		go func() {
			results <- r.attempt("reader", func() error {
				asked <- struct{}{}
				<-answer
				return AuthError{}
			})
		}()
	}
	for range signInTries {
		<-asked
	}
	for range 3 * signInTries {
		if !errors.As(<-results, new(PausedError)) {
			t.Fatal("a try past the count was not paused")
		}
	}
	close(answer)
	if len(asked) != 0 {
		t.Fatalf("%d more tries reached the mail server", len(asked))
	}

	accepted := newRefusals(1, signInWindow)
	for range 2 {
		if err := accepted.attempt("reader", func() error { return nil }); err != nil {
			t.Fatalf("an accepted try stayed counted: %v", err)
		}
	}
}
