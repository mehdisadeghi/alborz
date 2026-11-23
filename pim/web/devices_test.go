package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// A DAV client signs in with a device password in place of the mail password (ADR 107).
func TestDevicePasswordsSignInDAVClients(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	cal := c.CreateCalendar(t, "Plans", "")
	shown := regexp.MustCompile(`<code dir="ltr">([A-Z2-7]+)</code>`)
	create := func(name, access string) string {
		t.Helper()
		resp := c.Post(t, "settings.devices.create", url.Values{"name": {name}, "access": {access}})
		m := shown.FindStringSubmatch(resp.Text)
		if resp.StatusCode != http.StatusOK || m == nil {
			t.Fatalf("making a device password answered %s with no password shown", resp.Status)
		}
		return m[1]
	}
	as := func(password, method, path, body string) int {
		t.Helper()
		req, err := http.NewRequest(method, a.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth(User, password)
		req.Header.Set("Depth", "0")
		if method == http.MethodPut {
			req.Header.Set("Content-Type", "text/calendar")
		}
		return a.Client().Do(t, req).StatusCode
	}

	c.Page(t, "settings.devices.create")
	laptop, phone := create("laptop", "write"), create("phone", "read")
	c.Page(t, "settings.devices")
	if got := as(laptop, http.MethodPut, cal+"dentist.ics", Event("dentist", "Dentist")); got != http.StatusCreated {
		t.Fatalf("a PUT with a device password answered %d", got)
	}
	if got := as(phone, "PROPFIND", cal, ""); got != http.StatusMultiStatus {
		t.Fatalf("a PROPFIND with a read-only device password answered %d", got)
	}
	if got := as(phone, http.MethodPut, cal+"review.ics", Event("review", "Review")); got != http.StatusForbidden {
		t.Errorf("a PUT with a read-only device password answered %d", got)
	}
	// A client is told what its password may do, not left to learn it from a refusal.
	may := func(password string) string {
		t.Helper()
		req, err := http.NewRequest("PROPFIND", a.URL+cal, strings.NewReader(`<propfind xmlns="DAV:"><prop><current-user-privilege-set/></prop></propfind>`))
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth(User, password)
		req.Header.Set("Depth", "0")
		req.Header.Set("Content-Type", "application/xml")
		return a.Client().Do(t, req).Text
	}
	if got := may(laptop); !strings.Contains(got, "write") {
		t.Errorf("a device password is not told it may write: %s", got)
	}
	if got := may(phone); !strings.Contains(got, "read") || strings.Contains(got, "write") {
		t.Errorf("a read-only device password is told it may write: %s", got)
	}

	devices, _, err := a.Server.Devices.List(User)
	if err != nil || len(devices) != 2 {
		t.Fatalf("the account lists %d device passwords: %v", len(devices), err)
	}
	for _, d := range devices {
		if d.Name == "laptop" {
			if resp := c.Post(t, "settings.devices.revoke", url.Values{"device": {d.ID}}); resp.StatusCode != http.StatusFound {
				t.Fatalf("revoking answered %s", resp.Status)
			}
		}
	}
	if got := as(laptop, "PROPFIND", cal, ""); got != http.StatusUnauthorized {
		t.Errorf("a revoked device password answered %d", got)
	}

	if got := as(Password, "PROPFIND", cal, ""); got != http.StatusMultiStatus {
		t.Fatalf("the mail password answered %d", got)
	}
	if resp := c.Post(t, "settings.devices.only", url.Values{"only": {"1"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("taking device passwords only answered %s", resp.Status)
	}
	if got := as(Password, "PROPFIND", cal, ""); got != http.StatusUnauthorized {
		t.Errorf("the mail password answered %d with device passwords only", got)
	}
	if got := as(phone, "PROPFIND", cal, ""); got != http.StatusMultiStatus {
		t.Errorf("a device password answered %d with device passwords only", got)
	}
}
