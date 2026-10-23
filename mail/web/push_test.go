package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
)

// arrive delivers mail until a push reaches endpoint: the watcher takes its count when it starts to
// idle, and a message before that is part of the count.
func arrive(t *testing.T, a *App, push *PushService, endpoint string) Push {
	t.Helper()
	for range 10 {
		a.IMAP.Append(t, "INBOX", "From: <eve@example.org>\r\nTo: <"+User+">\r\nSubject: Hi\r\n\r\nHello\r\n")
		for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			for _, p := range push.Received() {
				if p.Endpoint == endpoint {
					return p
				}
			}
		}
	}
	t.Fatalf("no push reached %s", endpoint)
	return Push{}
}

func TestNotificationActionsRequireOriginAndActOnTheExactMessage(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t, Guarded(push.Client().Transport))
	c := a.SignIn(t, User)
	note := arrive(t, a, push, push.TurnOn(t, c))
	u, err := url.Parse(note.URL)
	if err != nil || len(note.Actions) != 2 {
		t.Fatalf("invalid notification: %+v", note)
	}
	uid := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/message/INBOX/"), "/notification")
	post := func(path, action, origin string, header bool) *Response {
		req, err := http.NewRequest(http.MethodPost, a.URL+path, strings.NewReader(url.Values{"action": {action}}.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if header {
			req.Header.Set("Alborz-Notification", "1")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		return c.Do(t, req)
	}
	read := note.Actions[0].URL
	if resp := post(read, "read", "", false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing header: %s", resp.Status)
	}
	if resp := post(read, "read", "https://other.example", true); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross origin: %s", resp.Status)
	}
	stale, _ := url.Parse(read)
	q := stale.Query()
	q.Set("validity", "4294967295")
	stale.RawQuery = q.Encode()
	if resp := post(stale.String(), "read", a.URL, true); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale UIDVALIDITY: %s", resp.Status)
	}
	if unseen := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"view": {"unread"}})); !slices.Contains(unseen, uid) {
		t.Fatalf("notification preview marked the mail read: %v", unseen)
	}
	if resp := post(read, "read", a.URL, true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("mark read: %s", resp.Status)
	}
	if unseen := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"view": {"unread"}})); slices.Contains(unseen, uid) {
		t.Fatalf("mark read left %s unread", uid)
	}
	if resp := post(note.Actions[1].URL, "delete", a.URL, true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %s %s", resp.Status, resp.Text)
	}
	if inbox := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX")); slices.Contains(inbox, uid) {
		t.Fatalf("delete left %s in INBOX", uid)
	}
	if trash := c.Page(t, "mail.mailbox", "Trash"); !strings.Contains(trash, "Hi") {
		t.Fatal("delete did not move the message to Trash")
	}
}

// New mail reaches every browser still signed in, with a bounded preview and signed by the instance's key; a
// browser signed out gets nothing.
func TestNewMailPushesEachSignedInBrowser(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t, Guarded(push.Client().Transport))
	here, gone := a.SignIn(t, User), a.SignIn(t, User)
	endpoint := push.TurnOn(t, here)
	away := push.TurnOn(t, gone)
	gone.Post(t, "auth.logout", nil)

	got := arrive(t, a, push, endpoint)
	if got.Title != T(t, "push.mail") || got.Body != User+"\nHi\nHello" {
		t.Fatalf("the push says %q / %q", got.Title, got.Body)
	}
	link, _ := url.Parse(got.URL)
	if !strings.HasPrefix(link.Path, "/message/INBOX/") || !strings.HasSuffix(link.Path, "/notification") || link.Query().Get("account") != User || link.Query().Get("validity") == "" {
		t.Fatalf("a click opens %q, want the exact message in its account", got.URL)
	}
	if len(got.Actions) != 2 || got.Actions[0].Action != "read" || got.Actions[1].Action != "delete" {
		t.Fatalf("missing useful actions: %+v", got.Actions)
	}
	if got.Header.Get("Topic") == "" || strings.Contains(got.Header.Get("Topic"), "test") {
		t.Fatalf("the topic is %q: it should be set and name no account", got.Header.Get("Topic"))
	}
	if key := instanceKey(t, here); got.Key != key {
		t.Fatalf("signed by %q, the settings page subscribes to %q", got.Key, key)
	}
	time.Sleep(200 * time.Millisecond)
	for _, p := range push.Received() {
		if p.Endpoint == away {
			t.Fatalf("a signed-out browser got %+v", p)
		}
	}
}

func TestNotificationOpenRefusesAReusedUIDAfterMailboxReset(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	upstream := a.IMAP.Dial(t)
	const mailbox = "NotificationReset"
	if err := upstream.Create(mailbox, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	uid := a.IMAP.Append(t, mailbox, "Subject: Original\r\n\r\nOriginal body\r\n")
	selected, err := upstream.Select(mailbox, nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	query := url.Values{"account": {User}, "validity": {fmt.Sprint(selected.UIDValidity)}}
	link := a.Path("mail.message.notification.open", mailbox, uid, query)
	if resp := c.Get(t, "mail.message.notification.open", mailbox, uid, query); resp.Header.Get("Location") != a.Path("mail.message", mailbox, uid, url.Values{"account": {User}}) {
		t.Fatalf("valid notification did not resolve to its message: %s %q", resp.Status, resp.Header.Get("Location"))
	}
	// Checking the link alone must not mark a message read before its validated redirect is followed.
	if rows := MessageUIDs(c.Page(t, "mail.mailbox", mailbox, url.Values{"view": {"unread"}})); !slices.Contains(rows, fmt.Sprint(uid)) {
		t.Fatal("validation marked original mail read")
	}
	if _, err := upstream.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := upstream.Delete(mailbox).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := upstream.Create(mailbox, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	replacement := a.IMAP.Append(t, mailbox, "Subject: Replacement\r\n\r\nUnrelated body\r\n")
	current, err := upstream.Select(mailbox, nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	if replacement != uid || current.UIDValidity == selected.UIDValidity {
		t.Fatalf("fixture did not reset UID namespace: old=%d/%d new=%d/%d", selected.UIDValidity, uid, current.UIDValidity, replacement)
	}
	assertUnread := func() {
		t.Helper()
		msgs, err := upstream.Fetch(imap.UIDSetNum(replacement), &imap.FetchOptions{Flags: true}).Collect()
		if err != nil || len(msgs) != 1 || slices.Contains(msgs[0].Flags, imap.FlagSeen) {
			t.Fatalf("stale notification read replacement: %v, %v", msgs, err)
		}
	}
	c.Follow(t, link)
	assertUnread()
	req, _ := http.NewRequest(http.MethodPost, a.URL+link, strings.NewReader("action=read"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Alborz-Notification", "1")
	if resp := c.Do(t, req); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale action: %s", resp.Status)
	}
	// This is the worker's fallback after the 409; it must remain safe too.
	c.Follow(t, link)
	assertUnread()
}

// A subscription the push service says is gone is dropped, not tried for ever.
func TestAGoneSubscriptionIsDropped(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t, Guarded(push.Client().Transport))
	c := a.SignIn(t, User)
	stays, drops := push.TurnOn(t, c), push.TurnOn(t, a.SignIn(t, User))
	push.Unsubscribe(drops)
	arrive(t, a, push, stays)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		page := c.Page(t, "settings.notifications")
		if n := strings.Count(page, `name="device"`); n == 1 {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("%d devices listed after the push service said one is gone, want 1", n)
		}
	}
}

func instanceKey(t *testing.T, c *Client) string {
	t.Helper()
	page := c.Page(t, "settings.notifications")
	_, rest, ok := strings.Cut(page, `id="push-settings" data-key="`)
	key, _, _ := strings.Cut(rest, `"`)
	if !ok || key == "" {
		t.Fatal("the settings page carries no key")
	}
	return key
}

// An item reaches a device once, however often it is pushed: twice by one alborz, and again after a restart.
func TestAnItemReachesADeviceOnceAcrossARestart(t *testing.T) {
	push := PushServer(t)
	opts := []Option{WithIMAP(IMAP(t)), DataDir(t.TempDir()), Guarded(push.Client().Transport)}
	a := Alborz(t, opts...)
	c := a.Client()
	if resp := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}, "remember-me": {"on"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("signing in: %s", resp.Status)
	}
	endpoint := push.TurnOn(t, c)
	note := func(uid int) web.PushNote {
		return web.PushNote{Key: "push.mail", Item: fmt.Sprintf("mail %s 1 %d", User, uid), Path: a.Path("mail.home")}
	}
	a.Server.Push(User, note(7))
	a.Server.Push(User, note(7))
	received(t, push, endpoint, 1)
	a.Close()

	a = Alborz(t, opts...)
	a.Server.Push(User, note(7))
	a.Server.Push(User, note(8))
	received(t, push, endpoint, 2)
	time.Sleep(200 * time.Millisecond)
	received(t, push, endpoint, 2)
}

// received waits for want pushes to endpoint and fails on more.
func received(t *testing.T, push *PushService, endpoint string, want int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		n := 0
		for _, p := range push.Received() {
			if p.Endpoint == endpoint {
				n++
			}
		}
		if n > want {
			t.Fatalf("%d pushes to the device, want %d", n, want)
		}
		if n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d pushes to the device, want %d", n, want)
		}
	}
}
