package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func turnOn(t *testing.T, c *Client, sub url.Values, account ...string) {
	t.Helper()
	user := User
	if len(account) > 0 {
		user = account[0]
	}
	resp := c.Post(t, "settings.notifications.create", sub, url.Values{"account": {user}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("turning notifications on: %s\n%s", resp.Status, NoticeBar(resp.Text))
	}
	if bar := NoticeBar(c.Follow(t, resp.Header.Get("Location"))); !Says(t, bar, "notice.notificationson") {
		t.Fatalf("no notice says notifications are on:\n%s", bar)
	}
}

var deviceRow = regexp.MustCompile(`(?s)<div class="list-item">.*?</form>`)

func devices(t *testing.T, c *Client) []string {
	t.Helper()
	return accountDevices(t, c, User)
}

// accountDevices are the account's rows of browsers with notifications, from the merged page.
func accountDevices(t *testing.T, c *Client, account string) []string {
	t.Helper()
	page := c.Page(t, "settings.notifications")
	if !strings.Contains(page, `name="account" value="`+account+`"`) {
		t.Fatalf("no notification controls for %s", account)
	}
	var rows []string
	for _, row := range deviceRow.FindAllString(page, -1) {
		if strings.Contains(row, `name="account" value="`+account+`"`) && strings.Contains(row, `name="device"`) {
			rows = append(rows, row)
		}
	}
	return rows
}

func TestNotificationAccountsUseCompactCurrentRows(t *testing.T) {
	push := PushServer(t)
	c := Alborz(t).SignIn(t, User)
	turnOn(t, c, push.Subscribe(t))
	page := c.Page(t, "settings.notifications")
	rows := accountDevices(t, c, User)
	if len(rows) != 1 {
		t.Fatalf("%d rows for this browser, want 1", len(rows))
	}
	if !Says(t, rows[0], "notifications.enabled") || !Says(t, rows[0], "notifications.since", Any) {
		t.Fatalf("the current row lacks its enabled state or subscription date:\n%s", rows[0])
	}
	if !regexp.MustCompile(`id="push-settings" data-key="[^"]+"`).MatchString(page) {
		t.Fatal("notification settings lost the shared browser key anchor")
	}
}

func TestOtherNotificationBrowserOffKeepsCurrentAndOtherAccount(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t)
	here, there := a.SignIn(t, User), a.SignIn(t, User)
	here.SignIn(t, User2)
	there.SignIn(t, User2)
	current, remote := push.Subscribe(t), push.Subscribe(t)
	for _, account := range []string{User, User2} {
		turnOn(t, here, current, account)
		turnOn(t, there, remote, account)
	}
	var others []string
	for _, row := range accountDevices(t, here, User) {
		if !Says(t, row, "notifications.enabled") {
			others = append(others, row)
		}
	}
	if len(others) != 1 {
		t.Fatalf("%d rows of other browsers, want 1", len(others))
	}
	fields := url.Values{}
	for _, field := range regexp.MustCompile(`name="(account|device)" value="([^"]+)"`).FindAllStringSubmatch(others[0], -1) {
		fields.Set(field[1], field[2])
	}
	if fields.Get("account") != User || fields.Get("device") == "" {
		t.Fatalf("remote-browser off form lost its account or device: %v", fields)
	}
	if resp := here.Post(t, "settings.notifications.off", fields); resp.StatusCode != http.StatusFound {
		t.Fatalf("turning the remote browser off: %s", resp.Status)
	}
	if rows := accountDevices(t, here, User); len(rows) != 1 || !Says(t, rows[0], "notifications.enabled") {
		t.Fatalf("turning the remote browser off changed this browser: %v", rows)
	}
	if rows := accountDevices(t, there, User); len(rows) != 1 || Says(t, rows[0], "notifications.enabled") {
		t.Fatalf("the remote browser is still enabled for the selected account: %v", rows)
	}
	for _, c := range []*Client{here, there} {
		rows := accountDevices(t, c, User2)
		marked := 0
		for _, row := range rows {
			if Says(t, row, "notifications.enabled") {
				marked++
			}
		}
		if len(rows) != 2 || marked != 1 {
			t.Fatalf("turning one account's remote browser off changed the other account: %v", rows)
		}
	}
}

// One page controls every account; sharing a browser subscription does not couple their on/off state.
func TestNotificationsArePerAccount(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	page := c.Page(t, "settings.notifications")
	for _, account := range []string{User, User2} {
		if !strings.Contains(page, `name="account" value="`+account+`"`) {
			t.Fatalf("the page has no notification form for %s", account)
		}
	}
	sub := push.Subscribe(t)
	for _, account := range []string{User, User2} {
		sub.Set("account", account)
		if resp := c.Post(t, "settings.notifications.create", sub); resp.StatusCode != http.StatusFound {
			t.Fatalf("turning on for %s: %s", account, resp.Status)
		}
	}
	rows := accountDevices(t, c, User)
	if len(rows) != 1 || len(accountDevices(t, c, User2)) != 1 {
		t.Fatal("each account should have this browser's device")
	}
	id := regexp.MustCompile(`name="device" value="([^"]+)"`).FindStringSubmatch(rows[0])[1]
	resp := c.Post(t, "settings.notifications.off", url.Values{"account": {User}, "device": {id}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("turning one account off: %s", resp.Status)
	}
	if len(accountDevices(t, c, User)) != 0 || len(accountDevices(t, c, User2)) != 1 {
		t.Fatal("turning one account off changed the other's subscription")
	}
	sub.Set("account", "outsider@test.local")
	resp = c.Post(t, "settings.notifications.create", sub)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("subscribing an account not signed in: %s", resp.Status)
	}
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
		if Says(t, row, "notifications.enabled") {
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
	if len(rows) != 1 || !Says(t, rows[0], "notifications.enabled") {
		t.Fatalf("after the other browser signed out, the rows are %v; its device should be gone", rows)
	}
	id := regexp.MustCompile(`name="device" value="([^"]+)"`).FindStringSubmatch(rows[0])[1]
	resp := here.Post(t, "settings.notifications.off", url.Values{"device": {id}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("turning off: %s", resp.Status)
	}
	if bar := NoticeBar(here.Follow(t, resp.Header.Get("Location"))); !Says(t, bar, "notice.notificationsoff") {
		t.Fatalf("no notice says notifications are off:\n%s", bar)
	}
	if rows := devices(t, here); len(rows) != 0 {
		t.Fatalf("after turning it off: %v", rows)
	}
}

// Turning this browser off also removes endpoints left behind by a subscription rotation.
func TestTurningOffCurrentBrowserRemovesAllItsSubscriptions(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t)
	here, there := a.SignIn(t, User), a.SignIn(t, User)
	// An old endpoint can remain if the browser rotated it without delivering a renewal.
	turnOn(t, here, push.Subscribe(t))
	turnOn(t, here, push.Subscribe(t))
	turnOn(t, there, push.Subscribe(t))
	var id string
	for _, row := range devices(t, here) {
		if Says(t, row, "notifications.enabled") {
			id = regexp.MustCompile(`name="device" value="([^"]+)"`).FindStringSubmatch(row)[1]
			break
		}
	}
	if id == "" {
		t.Fatal("no current browser to turn off")
	}
	resp := here.Post(t, "settings.notifications.off", url.Values{"account": {User}, "device": {id}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("turning this browser off: %s", resp.Status)
	}
	rows := devices(t, here)
	if len(rows) != 1 || Says(t, rows[0], "notifications.enabled") {
		t.Fatalf("turning off should leave only the other browser: %v", rows)
	}
}

// A form without a script carries no subscription, and one that is not a sound subscription is refused;
// neither is kept.
func TestNotificationsRefuseWhatIsNotASubscription(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t)
	c := a.SignIn(t, User)
	resp := c.Post(t, "settings.notifications.create", url.Values{})
	if resp.StatusCode != http.StatusUnprocessableEntity || !Says(t, NoticeBar(resp.Text), "notifications.needsscript") {
		t.Fatalf("without a script: %s\n%s", resp.Status, NoticeBar(resp.Text))
	}
	plain := push.Subscribe(t)
	plain.Set("endpoint", strings.Replace(plain.Get("endpoint"), "https:", "http:", 1))
	resp = c.Post(t, "settings.notifications.create", plain)
	if resp.StatusCode != http.StatusUnprocessableEntity || !Says(t, NoticeBar(resp.Text), "notifications.refused") {
		t.Fatalf("a plain http endpoint: %s\n%s", resp.Status, NoticeBar(resp.Text))
	}
	if rows := devices(t, c); len(rows) != 0 {
		t.Fatalf("a refused subscription was kept: %v", rows)
	}
}

// The browsers subscribed to the instance's key accept no other, so a restart keeps it.
func TestPushKeyOutlivesARestart(t *testing.T) {
	dir := t.TempDir()
	key := regexp.MustCompile(`id="push-settings" data-key="([^"]+)"`)
	a := Alborz(t, DataDir(dir))
	before := key.FindStringSubmatch(a.SignIn(t, User).Page(t, "settings.notifications"))
	a.Close()
	after := key.FindStringSubmatch(Alborz(t, DataDir(dir)).SignIn(t, User).Page(t, "settings.notifications"))
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
	turnOn(t, a.SignIn(t, User2), push.Subscribe(t), User2)
	before := devices(t, here)
	second := func() []string {
		return accountDevices(t, here, User2)
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
	if !Says(t, after[0], "notifications.enabled") {
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
