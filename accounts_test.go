package alborz_test

import (
	"context"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/internal/kv"
)

// An older build kept an account's settings under its address as typed; signing in with it brings them along.
func TestSignInBringsSettingsKeptUnderTheTypedAddress(t *testing.T) {
	a := Alborz(t)
	const typed = "A@Test.localdomain"
	ctx := context.Background()
	login, err := a.Server.SignIn(User, Password, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := login.Store().Put(ctx, "kept", "under the typed address"); err != nil {
		t.Fatal(err)
	}
	login.Close()
	db, err := a.Server.Data("alborz.settings", 2)
	if err != nil {
		t.Fatal(err)
	}
	names := a.Server.LoginKey.Names()
	if err := db.Update(func(tx kv.Tx) error {
		at := "alborz.settings/" + names.Of(User)
		if err := tx.Put("alborz.settings/"+names.Of(typed), tx.Get(at)); err != nil {
			return err
		}
		return tx.Delete(at)
	}); err != nil {
		t.Fatal(err)
	}

	login, err = a.Server.SignIn(typed, Password, "")
	if err != nil {
		t.Fatal(err)
	}
	defer login.Close()
	var got string
	if err := login.Store().Get(ctx, "kept", &got); login.Username() != User || got != "under the typed address" {
		t.Fatalf("%s signed in as %s with %q: %v", typed, login.Username(), got, err)
	}
}
