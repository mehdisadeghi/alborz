package keyring

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/internal/seal"
)

func ring(t *testing.T) (*Ring, kv.DB) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "alborz.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	root, err := seal.ParseRoot("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	return r, db
}

// A wrap opens only while its account's private key is at hand, and a file read again knows the pair.
func TestAWrapOpensWhileItsAccountHoldsItsKey(t *testing.T) {
	r, db := ring(t)
	own := bytes.Repeat([]byte{7}, seal.KeySize)
	release, err := r.Hold("a", own, true)
	if err != nil {
		t.Fatal(err)
	}
	w, ok, err := r.Wrap("a", []byte("collection key"), "info")
	if err != nil || !ok {
		t.Fatalf("Wrap = %v, %v", ok, err)
	}
	if got, ok := r.Unwrap(map[string]Wrap{"a": w}, "info"); !ok || string(got) != "collection key" {
		t.Fatalf("held: Unwrap = %q, %v", got, ok)
	}
	if _, ok := r.Unwrap(map[string]Wrap{"a": w}, "other"); ok {
		t.Fatal("a wrap opened under another info")
	}
	release()
	if !r.Locked("a") {
		t.Fatal("released, the account is not locked")
	}
	if _, ok := r.Unwrap(map[string]Wrap{"a": w}, "info"); ok {
		t.Fatal("released, the wrap still opens")
	}
	if _, err := r.For("a", "settings"); !errors.Is(err, ErrLocked) {
		t.Fatalf("released: For = %v, want ErrLocked", err)
	}

	again, err := New(db, r.root)
	if err != nil {
		t.Fatal(err)
	}
	if !again.On("a") || again.ID("a") != r.ID("a") {
		t.Fatal("read again, the pair or its state changed")
	}
	if _, err := again.Hold("a", own, false); err != nil {
		t.Fatal(err)
	}
	if got, ok := again.Unwrap(map[string]Wrap{"a": w}, "info"); !ok || string(got) != "collection key" {
		t.Fatal("held again, the wrap does not open")
	}
	if again.On("a") {
		t.Fatal("turned off, the account's values still go under its own key")
	}
}

// A plain account's private key opens under the instance's key, with no login.
func TestAPlainAccountsWrapOpensWithoutItsLogin(t *testing.T) {
	r, _ := ring(t)
	release, err := r.Hold("b", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	w, ok, _ := r.Wrap("b", []byte("k"), "i")
	if _, opened := r.Unwrap(map[string]Wrap{"b": w}, "i"); !ok || !opened || r.Locked("b") {
		t.Fatal("a plain account's wrap does not open without its login")
	}
	if _, ok, _ := r.Wrap("nobody", []byte("k"), "i"); ok {
		t.Fatal("an account with no pair was wrapped to")
	}
}

// A new own key replaces the pair; a wrap to the old one opens until the account has settled.
func TestANewKeyRetiresThePair(t *testing.T) {
	r, _ := ring(t)
	if _, err := r.Hold("a", bytes.Repeat([]byte{7}, seal.KeySize), true); err != nil {
		t.Fatal(err)
	}
	was := r.ID("a")
	w, _, _ := r.Wrap("a", []byte("k"), "i")
	if err := r.Turn("a", bytes.Repeat([]byte{8}, seal.KeySize), true); err != nil {
		t.Fatal(err)
	}
	if r.ID("a") == was {
		t.Fatal("a new key kept the pair")
	}
	if _, ok := r.Unwrap(map[string]Wrap{"a": w}, "i"); !ok {
		t.Fatal("a wrap to the retired pair does not open before the account settled")
	}
	if err := r.Settled("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Unwrap(map[string]Wrap{"a": w}, "i"); ok {
		t.Fatal("a wrap to the retired pair opens after the account settled")
	}
}
