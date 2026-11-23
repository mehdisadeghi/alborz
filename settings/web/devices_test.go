package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// A device password opens an encrypted account's key with no login and a reset mail password, a scoped one
// too, which the server then keeps to its scope (ADR 109); turning encryption off deletes them (ADR 107).
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
	a.Close()
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
}
