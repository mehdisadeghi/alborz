package web_test

import (
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/internal/seal"
)

// A device password opens an encrypted account's key with no login and a reset mail password. A scoped one
// wraps the key of its collection and not the account's, reads with that alone and leaves the account locked
// (ADR 109). Turning encryption off or on deletes those made before (ADR 107).
func TestADevicePasswordOpensAnEncryptedAccount(t *testing.T) {
	imap, dir := IMAP(t), t.TempDir()
	opts := []Option{WithIMAP(imap), DataDir(dir)}
	a := Alborz(t, opts...)
	c := a.SignIn(t, User)
	cal := c.CreateCalendar(t, "Personal", "")
	a.Put(t, cal+"review.ics", "text/calendar", Event("review", "Budget review"))
	other := c.CreateCalendar(t, "Other", "")
	a.Put(t, other+"visit.ics", "text/calendar", Event("visit", "Site visit"))
	encrypt(t, c)
	shown := regexp.MustCompile(`<code dir="ltr">([A-Z2-7]+)</code>`)
	scoped := shown.FindStringSubmatch(c.Post(t, "settings.devices.create",
		url.Values{"name": {"y"}, "access": {"read"}, "scope": {"some"}, "collection": {other}, "account": {User}}).Text)
	if scoped == nil {
		t.Fatal("making a scoped device password showed no password")
	}
	resp := c.Post(t, "settings.devices.create", url.Values{"name": {"x"}, "access": {"write"}, "account": {User}})
	m := shown.FindStringSubmatch(resp.Text)
	if m == nil {
		t.Fatalf("making a device password answered %s with no password shown", resp.Status)
	}
	devices, _, err := a.Server.Devices.List(User)
	if err != nil || len(devices) != 2 || devices[1].Name != "y" {
		t.Fatalf("the account lists %+v: %v", devices, err)
	}
	held := devices[1].Keys(scoped[1])
	if devices[1].ByDevice != nil || len(held) != 1 {
		t.Fatalf("the scoped device password wraps the account's key, or the keys of %d collections", len(held))
	}
	a.Close()
	for _, key := range held {
		if n := opened(t, dir, key); n != 1 {
			t.Fatalf("the key the scoped device password wraps opens %d collections in the data file", n)
		}
	}
	imap.SetPassword(User, "reset")
	a = Alborz(t, opts...)
	read := func(password, path string) *Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, a.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth(User, password)
		return a.Client().Do(t, req)
	}
	if got := read(scoped[1], other+"visit.ics").Text; !strings.Contains(got, "Site visit") {
		t.Fatalf("a scoped device password read %q", got)
	}
	if got := read(scoped[1], cal+"review.ics"); got.StatusCode != http.StatusNotFound {
		t.Fatalf("a scoped device password read a calendar out of its scope: %s", got.Status)
	}
	if keys := a.Server.Keys; !keys.Locked(keys.Name(User)) {
		t.Fatal("the scoped device password opened the account's key")
	}
	if got := read(m[1], cal+"review.ics").Text; !strings.Contains(got, "Budget review") {
		t.Fatalf("a device password read %q", got)
	}
	c, _ = signInWith(t, a, "reset")
	if !c.Listed(t)[Here+cal] {
		t.Fatal("signed in with the reset password, the calendar does not read")
	}
	if resp := c.Post(t, "settings.encryption.off", url.Values{"password": {"reset"}, "account": {User}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("turning encryption off answered %s", resp.Status)
	}
	if d, _, err := a.Server.Devices.List(User); err != nil || len(d) != 0 {
		t.Fatalf("turned off, %d device passwords are kept: %v", len(d), err)
	}
	c.Post(t, "settings.devices.create", url.Values{"name": {"z"}, "access": {"read"}, "scope": {"some"}, "collection": {other}, "account": {User}})
	if d, _, err := a.Server.Devices.List(User); err != nil || len(d) != 1 {
		t.Fatalf("made with encryption off, %d device passwords are kept: %v", len(d), err)
	}
	if resp := c.Post(t, "settings.encryption", url.Values{"password": {"reset"}, "account": {User}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("turning encryption on answered %s", resp.Status)
	}
	if d, _, err := a.Server.Devices.List(User); err != nil || len(d) != 0 {
		t.Fatalf("turned on, %d device passwords are kept: %v", len(d), err)
	}
}

// opened counts the collections in the data file of a stopped alborz that key opens.
func opened(t *testing.T, dir string, key []byte) int {
	t.Helper()
	db, err := bolt.Open(filepath.Join(dir, "alborz.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := 0
	err = db.View(func(tx kv.Tx) error {
		return tx.Scan("alborz.pim/collections/", func(_ string, v []byte) error {
			if _, err := seal.NewAccountKey([seal.KeySize]byte(key)).Open(v); err == nil {
				n++
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
