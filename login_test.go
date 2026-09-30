package alborz

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestExpiryUnlistsTheLogin(t *testing.T) {
	sm := newLoginManager(nil, nil, nil, nil, nil, nil, nil)
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

func TestEnvelopeOpensOnlyWithTheMailPassword(t *testing.T) {
	login := func(password string) *Login { return &Login{password: password} }
	e, err := login("first").seal("app-password")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := login("first").open(e); err != nil || plain != "app-password" {
		t.Fatalf("with the password it was sealed under: %q, %v", plain, err)
	}
	if _, err := login("second").open(e); err != ErrSealed {
		t.Fatalf("after a password change: %v, want ErrSealed", err)
	}
}

type mapStore map[string][]byte

func (m mapStore) Get(_ context.Context, key string, out any) error {
	raw, ok := m[key]
	if !ok {
		return ErrNoStoreEntry
	}
	return json.Unmarshal(raw, out)
}

func (m mapStore) Put(_ context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	m[key] = raw
	return err
}

func TestEnvelopeFollowsAPasswordChange(t *testing.T) {
	ctx, store := context.Background(), mapStore{}
	if err := (&Login{password: "first", store: store}).SetHTTPPassword(ctx, "app-password"); err != nil {
		t.Fatal(err)
	}
	if err := (&Login{password: "first", store: store}).rewrap(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if plain, err := (&Login{password: "second", store: store}).HTTPPassword(ctx); err != nil || plain != "app-password" {
		t.Fatalf("under the new password: %q, %v", plain, err)
	}
	if _, err := (&Login{password: "first", store: store}).HTTPPassword(ctx); err != ErrSealed {
		t.Fatalf("under the old password: %v, want ErrSealed", err)
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
