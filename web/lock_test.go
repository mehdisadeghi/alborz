package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.mehdix.org/alborz"
)

func TestASessionLocksOnlyWithAPasskeyAndOnlyAfterTimeAway(t *testing.T) {
	s, _ := newSession()
	now := time.Now()
	if s.Locked(now.Add(365 * 24 * time.Hour)) {
		t.Fatal("a session with no passkey locked")
	}

	s.addPasskey(alborz.Passkey{Name: "key"})
	s.SetLockAfter(10 * time.Minute)
	s.act(now)
	if s.Locked(now.Add(9 * time.Minute)) {
		t.Fatal("locked inside its lock time")
	}
	if !s.Locked(now.Add(11 * time.Minute)) {
		t.Fatal("still open past its lock time")
	}
	s.act(now.Add(12 * time.Minute))
	if !s.Locked(now.Add(12 * time.Minute)) {
		t.Fatal("a request unlocked a locked session")
	}
	s.unlock(now.Add(13 * time.Minute))
	if s.Locked(now.Add(14 * time.Minute)) {
		t.Fatal("locked right after unlocking")
	}

	if back := sessionFromRecord(s.record()); !back.Locked(time.Now()) {
		t.Fatal("a session with a passkey came back from its record unlocked")
	}
}

func TestCookieSharingTabsStayLockedWithoutLosingRememberedAccounts(t *testing.T) {
	s, secret := newSession()
	s.addPasskey(alborz.Passkey{Name: "key"})
	s.Remember("one@example.org", []byte("sealed-one"))
	s.Remember("two@example.org", []byte("sealed-two"))
	proof := bytes.Clone(s.proof)
	server := &Server{Sessions: newSessions(nil, nil), routes: routes{names: map[string]string{"auth.unlock": "/unlock"}}}
	server.Sessions.put(s)
	cookie := &http.Cookie{Name: sessionCookieName, Value: s.ID + "." + secret}
	request := func(background bool) (*Context, *httptest.ResponseRecorder) {
		req := httptest.NewRequest(http.MethodGet, "/private", nil)
		req.AddCookie(cookie)
		if background {
			req.Header.Set(unaskedHeader, "1")
		}
		out := httptest.NewRecorder()
		return &Context{Server: server, request: req, response: &Response{ResponseWriter: out}}, out
	}
	before := s.active
	ctx, _ := request(true)
	if err := Activity(func(*Context) error { return nil })(ctx); err != nil {
		t.Fatal(err)
	}
	if !s.active.Equal(before) {
		t.Fatal("a background state check extended lock activity")
	}
	// The locking tab changes the shared session, not its account credentials or cookie.
	ctx.Session().LockNow()
	for range 2 {
		ctx, out := request(false)
		if err := Lock(func(*Context) error { t.Fatal("a new same-cookie client reached protected data"); return nil })(ctx); err != nil {
			t.Fatal(err)
		}
		if out.Code != http.StatusSeeOther || out.Header().Get("Location") != "/unlock?next=/private" {
			t.Fatalf("locked tab: %d %q", out.Code, out.Header().Get("Location"))
		}
		if len(out.Result().Cookies()) != 0 {
			t.Fatal("locking replaced the session cookie")
		}
	}
	if !bytes.Equal(proof, s.proof) || len(s.remember) != 2 || !s.opens(secret) {
		t.Fatal("locking discarded account credentials or the session")
	}
}

func TestLiveChangeDoesNotExposeAFrameWhenLockWinsTheWake(t *testing.T) {
	s, _ := newSession()
	s.addPasskey(alborz.Passkey{Name: "key"})
	out := httptest.NewRecorder()
	res := &Response{ResponseWriter: out}
	stop := writeLiveChange(res, s, alborz.Change{Account: "private@example.org", Mailbox: "Secret"}, func(string) bool {
		// Model a lock landing while the event's account ownership is being checked.
		s.LockNow()
		return true
	})
	if !stop || out.Body.String() != "event: locked\ndata:\n\n" {
		t.Fatalf("stream leaked after locking: %q (stop=%v)", out.Body.String(), stop)
	}
}
