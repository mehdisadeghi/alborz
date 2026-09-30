package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func turnOn(t *testing.T, c *Client, sub url.Values) {
	t.Helper()
	resp := c.Post(t, "settings.notifications.create", sub)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("turning notifications on: %s\n%s", resp.Status, NoticeBar(resp.Text))
	}
	if bar := NoticeBar(c.Follow(t, resp.Header.Get("Location"))); !strings.Contains(bar, "Notifications turned on for this browser.") {
		t.Fatalf("no notice says notifications are on:\n%s", bar)
	}
}

var deviceRow = regexp.MustCompile(`(?s)<div class="list-item">.*?</form>`)

func devices(t *testing.T, c *Client) []string {
	t.Helper()
	return deviceRow.FindAllString(c.Page(t, "settings.notifications"), -1)
}

// Each browser is a row of its own and the one asking is marked; signing a browser out, or turning
// one off, takes its row away.
func TestNotificationsTurnOnAndOff(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t)
	here, there := a.SignIn(t, User), a.SignIn(t, User)
	turnOn(t, here, push.Subscribe(t))
	turnOn(t, there, push.Subscribe(t))

	rows := devices(t, here)
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	marked := 0
	for _, row := range rows {
		if strings.Contains(row, "This browser") {
			marked++
		}
	}
	if marked != 1 {
		t.Fatalf("%d rows marked as this browser, want 1:\n%s", marked, rows)
	}

	if resp := there.Post(t, "auth.logout", nil); resp.StatusCode != http.StatusFound {
		t.Fatalf("signing out: %s", resp.Status)
	}
	rows = devices(t, here)
	if len(rows) != 1 || !strings.Contains(rows[0], "This browser") {
		t.Fatalf("after the other browser signed out, the rows are %v; its device should be gone", rows)
	}
	id := regexp.MustCompile(`name="device" value="([^"]+)"`).FindStringSubmatch(rows[0])[1]
	resp := here.Post(t, "settings.notifications.off", url.Values{"device": {id}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("turning off: %s", resp.Status)
	}
	if bar := NoticeBar(here.Follow(t, resp.Header.Get("Location"))); !strings.Contains(bar, "Notifications turned off.") {
		t.Fatalf("no notice says notifications are off:\n%s", bar)
	}
	if rows := devices(t, here); len(rows) != 0 {
		t.Fatalf("after turning it off: %v", rows)
	}
}

// A form without a script carries no subscription, and one that is not a sound subscription is refused;
// neither is kept.
func TestNotificationsRefuseWhatIsNotASubscription(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t)
	c := a.SignIn(t, User)
	resp := c.Post(t, "settings.notifications.create", url.Values{})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(NoticeBar(resp.Text), "Notifications need JavaScript.") {
		t.Fatalf("without a script: %s\n%s", resp.Status, NoticeBar(resp.Text))
	}
	plain := push.Subscribe(t)
	plain.Set("endpoint", strings.Replace(plain.Get("endpoint"), "https:", "http:", 1))
	resp = c.Post(t, "settings.notifications.create", plain)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(NoticeBar(resp.Text), "subscription was not accepted") {
		t.Fatalf("a plain http endpoint: %s\n%s", resp.Status, NoticeBar(resp.Text))
	}
	if rows := devices(t, c); len(rows) != 0 {
		t.Fatalf("a refused subscription was kept: %v", rows)
	}
}

// The browsers subscribed to the instance's key accept no other, so a restart keeps it.
func TestPushKeyOutlivesARestart(t *testing.T) {
	dir := t.TempDir()
	key := regexp.MustCompile(`data-key="([^"]+)"`)
	a := Alborz(t, DataDir(dir))
	before := key.FindStringSubmatch(a.SignIn(t, User).Page(t, "settings.notifications.create"))
	a.Close()
	after := key.FindStringSubmatch(Alborz(t, DataDir(dir)).SignIn(t, User).Page(t, "settings.notifications.create"))
	if before == nil || after == nil || before[1] != after[1] {
		t.Fatalf("the push key changed over a restart: %v, %v", before, after)
	}
}

// A subscription the push service replaced takes the old one's place in every account of the browser
// that had it, and not in one signed in elsewhere.
func TestARenewedSubscriptionReplacesTheOld(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t)
	here := a.SignIn(t, User)
	here.SignIn(t, User2)
	old := push.Subscribe(t)
	turnOn(t, here, old)
	resp := here.Post(t, "settings.notifications.create", old, url.Values{"account": {User2}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("turning on for the second account: %s", resp.Status)
	}
	turnOn(t, a.SignIn(t, User2), push.Subscribe(t))
	before := devices(t, here)
	second := func() []string {
		return deviceRow.FindAllString(here.Page(t, "settings.notifications", url.Values{"account": {User2}}), -1)
	}
	beforeSecond := second()

	renewed := push.Subscribe(t)
	renewed.Set("old", old.Get("endpoint"))
	if resp := here.Post(t, "settings.notifications.create", renewed); resp.StatusCode != http.StatusFound {
		t.Fatalf("renewing: %s", resp.Status)
	}
	after := devices(t, here)
	if len(before) != 1 || len(after) != 1 || after[0] == before[0] {
		t.Fatalf("before %d rows, after %d; the row should be replaced, not added:\n%v\n%v", len(before), len(after), before, after)
	}
	if !strings.Contains(after[0], "This browser") {
		t.Fatalf("the renewed row is not this browser's: %s", after[0])
	}
	afterSecond := second()
	if len(beforeSecond) != 2 || len(afterSecond) != 2 {
		t.Fatalf("the second account lists %d then %d devices, want this browser's and the other's", len(beforeSecond), len(afterSecond))
	}
	if afterSecond[0] == beforeSecond[0] && afterSecond[1] == beforeSecond[1] {
		t.Fatal("the second account's device was not renewed")
	}
}
