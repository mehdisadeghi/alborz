package alborz

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz/internal/keyring"
	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/internal/seal"
)

func TestExpiryUnlistsTheLogin(t *testing.T) {
	sm := newLoginManager(nil, nil, nil, nil, nil, nil, nil, nil, nil, LoginDuration, nil)
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

// plainManager holds its accounts' keys in a file of the test's, none of them encrypted.
func plainManager(t *testing.T) *loginManager {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), storeFile), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	root, err := seal.ParseRoot("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := keyring.New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	return newLoginManager(nil, nil, nil, nil, nil, nil, nil, keys, nil, LoginDuration, nil)
}

func TestEnvelopeOpensOnlyWithTheMailPassword(t *testing.T) {
	sm := plainManager(t)
	login := func(password string) *Login { return &Login{password: password, manager: sm} }
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
	ctx, store, sm := context.Background(), mapStore{}, plainManager(t)
	login := func(password string) *Login { return &Login{password: password, store: store, manager: sm} }
	if err := login("first").SetHTTPPassword(ctx, "app-password"); err != nil {
		t.Fatal(err)
	}
	if err := login("first").rewrap(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if plain, err := login("second").HTTPPassword(ctx); err != nil || plain != "app-password" {
		t.Fatalf("under the new password: %q, %v", plain, err)
	}
	if _, err := login("first").HTTPPassword(ctx); err != ErrSealed {
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

func TestAFileIsRefusedUnderAnotherKeyOrFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, storeFile)
	root := func(text string) seal.Root {
		r, err := seal.ParseRoot(text)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	sealer, other := root("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA="), root("q2cV8ZlWmQ2FQm8m2JbM0m9m8p0v1l9c3Qq0a5pX0kU=")
	check := func(r seal.Root, change func(kv.Tx) error) error {
		db, _, err := openData(dir, "test", t.Logf)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if change != nil {
			if err := db.Update(change); err != nil {
				t.Fatal(err)
			}
		}
		key := r.Key(sessionsOwner)
		if err := CheckKey(db, path, r, map[string]*seal.Key{sessionsOwner + "/": key}); err != nil {
			return err
		}
		records := openSessionRecords(db, key)
		if _, ok := records.Load("kept"); !ok {
			return records.Save(&SessionRecord{ID: "kept"})
		}
		return nil
	}
	if err := check(sealer, nil); err != nil {
		t.Fatal(err)
	}
	if err := check(other, nil); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("another key: %v, want a refusal naming %s", err, path)
	}
	var kept bool
	err := check(sealer, func(tx kv.Tx) error {
		kept = tx.Get(sessionRecordKey("kept")) != nil
		return nil
	})
	if err != nil || !kept {
		t.Fatalf("the sealing key after a refusal: %v, record kept: %v", err, kept)
	}
	fernet := func(tx kv.Tx) error { return tx.Put(keyCheckKey, []byte("gAAAAABnAAAAAAAAAAAAAAAAAAAAAA==")) }
	if err := check(sealer, fernet); err == nil || !strings.Contains(err.Error(), "earlier alborz") {
		t.Fatalf("an earlier format: %v, want a refusal saying so", err)
	}
}
