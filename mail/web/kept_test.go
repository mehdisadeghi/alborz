package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// idle is the tests' login idle limit, long enough for a sign-in and a page.
const idle = 500 * time.Millisecond

func logins(a *App, user string) int {
	n := 0
	for _, l := range a.Server.Logins() {
		if l.Username() == user {
			n++
		}
	}
	return n
}

// awaitLogins waits for the account's live logins to come to want.
func awaitLogins(t *testing.T, a *App, want int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); logins(a, User) != want; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d logins of %s, want %d", logins(a, User), User, want)
		}
	}
}

// A browser with notifications on keeps its login past the idle limit, and new mail still reaches it.
func TestADeviceKeepsItsLoginForNotifications(t *testing.T) {
	push := PushServer(t)
	a := Alborz(t, Guarded(push.Client().Transport), LoginIdle(idle))
	endpoint := push.TurnOn(t, a.SignIn(t, User))
	time.Sleep(3 * idle)
	if n := logins(a, User); n != 1 {
		t.Fatalf("%d logins after the idle limit, want the one kept", n)
	}
	arrive(t, a, push, endpoint)
}

// With no browser to notify, a login ends at the idle limit as it always has.
func TestWithoutADeviceALoginEndsWhenIdle(t *testing.T) {
	a := Alborz(t, LoginIdle(idle))
	a.SignIn(t, User)
	awaitLogins(t, a, 0)
}

// Turning the device off from another browser lets the kept login go at once, not at its next idle check:
// with a check a second apart, waiting for it would pass a quarter second only one time in four.
func TestTurningTheDeviceOffEndsTheKeptLogin(t *testing.T) {
	const idle = time.Second
	push := PushServer(t)
	a := Alborz(t, Guarded(push.Client().Transport), LoginIdle(idle))
	push.TurnOn(t, a.SignIn(t, User))
	other := a.Client()
	if resp := other.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}, "remember-me": {"on"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("signing in: %s", resp.Status)
	}
	time.Sleep(3 * idle)
	page := other.Page(t, "settings.notifications")
	awaitLogins(t, a, 2)
	id := regexp.MustCompile(`name="device" value="([^"]+)"`).FindStringSubmatch(page)
	if id == nil {
		t.Fatal("the other browser lists no device")
	}
	start := time.Now()
	if resp := other.Post(t, "settings.notifications.off", url.Values{"device": {id[1]}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("turning off: %s", resp.Status)
	}
	awaitLogins(t, a, 1)
	if waited := time.Since(start); waited > idle/4 {
		t.Fatalf("the kept login lasted %v after the device went off", waited)
	}
}
