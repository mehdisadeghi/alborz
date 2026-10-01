package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/web"
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

// New mail reaches every browser still signed in, content-free and signed by the instance's key; a
// browser signed out gets nothing.
func TestNewMailPushesEachSignedInBrowser(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t, Guarded(push.Client().Transport))
	here, gone := a.SignIn(t, User), a.SignIn(t, User)
	endpoint := push.TurnOn(t, here)
	away := push.TurnOn(t, gone)
	gone.Post(t, "auth.logout", nil)

	got := arrive(t, a, push, endpoint)
	if got.Title != T(t, "push.mail") || got.Body != User {
		t.Fatalf("the push says %q / %q", got.Title, got.Body)
	}
	if strings.Contains(got.Title+got.Body, "Hi") || strings.Contains(got.Title+got.Body, "eve@") {
		t.Fatalf("the push carries the message: %+v", got)
	}
	if want := a.Path("mail.mailbox", "INBOX") + web.AccountQuery(User); got.URL != want {
		t.Fatalf("a click opens %q, want the account's inbox", got.URL)
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
	page := c.Page(t, "settings.notifications.create")
	_, rest, ok := strings.Cut(page, `data-key="`)
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
