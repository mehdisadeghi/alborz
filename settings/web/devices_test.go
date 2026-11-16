package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// A device password opens an encrypted account's key with no login and a reset mail password; turning
// encryption off deletes it (ADR 107).
func TestADevicePasswordOpensAnEncryptedAccount(t *testing.T) {
	imap, dir := IMAP(t), t.TempDir()
	opts := []Option{WithIMAP(imap), DataDir(dir)}
	a := Alborz(t, opts...)
	c := a.SignIn(t, User)
	cal := c.CreateCalendar(t, "Personal", "")
	a.Put(t, cal+"review.ics", "text/calendar", Event("review", "Budget review"))
	encrypt(t, c)
	resp := c.Post(t, "settings.devices.create", url.Values{"name": {"x"}, "access": {"write"}, "account": {User}})
	m := regexp.MustCompile(`<code dir="ltr">([A-Z2-7]+)</code>`).FindStringSubmatch(resp.Text)
	if m == nil {
		t.Fatalf("making a device password answered %s with no password shown", resp.Status)
	}
	a.Close()
	imap.SetPassword(User, "reset")
	a = Alborz(t, opts...)
	req, err := http.NewRequest(http.MethodGet, a.URL+cal+"review.ics", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(User, m[1])
	if got := a.Client().Do(t, req).Text; !strings.Contains(got, "Budget review") {
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
