package web

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/seal"
)

type sessionRecordsForTest struct {
	records map[string]*alborz.SessionRecord
	fail    error
}

func (r *sessionRecordsForTest) Load(id string) (*alborz.SessionRecord, bool) {
	s, ok := r.records[id]
	return s, ok
}
func (r *sessionRecordsForTest) Save(s *alborz.SessionRecord) error {
	if r.fail != nil {
		return r.fail
	}
	r.records[s.ID] = s
	return nil
}
func (r *sessionRecordsForTest) Delete(id string) error { delete(r.records, id); return nil }
func (r *sessionRecordsForTest) Sweep(time.Time) error  { return nil }
func (r *sessionRecordsForTest) Each(f func(*alborz.SessionRecord)) error {
	for _, s := range r.records {
		f(s)
	}
	return nil
}

func sessionRequest(ss *Sessions, s *Session, secret string) (*Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/private", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: s.ID + "." + secret})
	out := httptest.NewRecorder()
	return &Context{Server: &Server{Sessions: ss}, request: req, response: &Response{ResponseWriter: out}}, out
}

func TestFailedRememberedRenewalDoesNotExtendTheCookieOrSession(t *testing.T) {
	s, secret := newSession()
	s.expires = time.Now().Add(time.Hour)
	s.Remember("one@example.org", sessionKey(secret).Seal([]byte("password")))
	records := &sessionRecordsForTest{records: map[string]*alborz.SessionRecord{s.ID: s.record()}, fail: errors.New("disk full")}
	ss := newSessions(records, nil)
	ss.put(s)
	ctx, out := sessionRequest(ss, s, secret)
	ctx.lookupSession()
	before := s.expires
	if err := ctx.renewRemembered(time.Now()); !errors.Is(err, records.fail) {
		t.Fatalf("renewal error: %v", err)
	}
	if !s.expires.Equal(before) || !records.records[s.ID].Expires.Equal(before) || len(out.Result().Cookies()) != 0 {
		t.Fatal("failed renewal promised a longer remembered sign-in")
	}
}

func TestARequestCannotRenewARevokedRememberedSession(t *testing.T) {
	s, secret := newSession()
	s.expires = time.Now().Add(time.Hour)
	s.Remember("one@example.org", sessionKey(secret).Seal([]byte("password")))
	records := &sessionRecordsForTest{records: map[string]*alborz.SessionRecord{s.ID: s.record()}}
	ss := newSessions(records, nil)
	ss.put(s)
	ctx, out := sessionRequest(ss, s, secret)
	ctx.lookupSession()
	if err := ss.delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if err := ctx.renewRemembered(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ss.Save(s); err != nil {
		t.Fatal(err)
	}
	if _, ok := records.Load(s.ID); ok {
		t.Fatal("an already-running request resurrected a revoked session")
	}
	if len(out.Result().Cookies()) != 0 {
		t.Fatal("a revoked session received a renewed cookie")
	}
}

func TestExpiredRememberedSessionCannotReturnFromStorage(t *testing.T) {
	s, secret := newSession()
	s.created = time.Now().Add(-31 * 24 * time.Hour)
	s.seen = s.created
	s.expires = time.Now().Add(-time.Second)
	s.Remember("one@example.org", sessionKey(secret).Seal([]byte("password")))
	records := &sessionRecordsForTest{records: map[string]*alborz.SessionRecord{s.ID: s.record()}}
	ss := newSessions(records, nil)
	ctx, _ := sessionRequest(ss, s, secret)
	if ctx.lookupSession() != nil {
		t.Fatal("an expired persisted session was restored")
	}
}

func TestRememberedLookupDoesNotCountAsActivity(t *testing.T) {
	s, secret := newSession()
	s.seen = time.Now().Add(-24 * time.Hour)
	s.expires = time.Now().Add(time.Hour)
	s.Remember("one@example.org", sessionKey(secret).Seal([]byte("password")))
	ss := newSessions(nil, nil)
	ss.put(s)
	before := s.lastSeen()
	ctx, out := sessionRequest(ss, s, secret)
	ctx.request.Header.Set(unaskedHeader, "1")
	if ctx.lookupSession() == nil {
		t.Fatal("valid session was lost")
	}
	if !s.lastSeen().Equal(before) {
		t.Fatal("a background lookup renewed the remembered session")
	}
	if len(out.Result().Cookies()) != 0 {
		t.Fatal("a background lookup refreshed the cookie")
	}
}

func TestRememberingAfterFreshAuthenticationDoesNotUseTheOldCookieAge(t *testing.T) {
	s, secret := newSession()
	s.created = time.Now().Add(-90 * 24 * time.Hour)
	records := &sessionRecordsForTest{records: map[string]*alborz.SessionRecord{}}
	ss := newSessions(records, nil)
	ss.put(s)
	ctx, out := sessionRequest(ss, s, secret)
	if ctx.lookupSession() == nil {
		t.Fatal("active nonremembered session was lost")
	}
	if err := ctx.SetLoginToken("one@example.org", "fresh password"); err != nil {
		t.Fatal(err)
	}
	if time.Until(s.expires) < 60*24*time.Hour-time.Minute || len(out.Result().Cookies()) != 1 {
		t.Fatal("fresh authentication did not start the remembered lifetime")
	}
	if password, ok := ctx.openRemembered(s.remember["one@example.org"], s.expires, time.Now()); !ok || password != "fresh password" {
		t.Fatal("fresh password was not kept")
	}
}

func TestFailedInitialRememberDoesNotKeepAnUnpersistedCredential(t *testing.T) {
	s, secret := newSession()
	records := &sessionRecordsForTest{records: map[string]*alborz.SessionRecord{}, fail: errors.New("disk full")}
	ss := newSessions(records, nil)
	ss.put(s)
	ctx, out := sessionRequest(ss, s, secret)
	ctx.lookupSession()
	if err := ctx.SetLoginToken("one@example.org", "password"); !errors.Is(err, records.fail) {
		t.Fatalf("remember error: %v", err)
	}
	if len(s.remember) != 0 || !s.expires.IsZero() || len(out.Result().Cookies()) != 0 {
		t.Fatal("failed persistence still changed the remembered session")
	}
}

func TestRememberedDeadlineGovernsLiveAndStoredSessions(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(map[bool]string{true: "live", false: "stored"}[live], func(t *testing.T) {
			s, secret := newSession()
			s.expires = time.Now().Add(-time.Second)
			s.Remember("one@example.org", sessionKey(secret).Seal([]byte("password")))
			records := &sessionRecordsForTest{records: map[string]*alborz.SessionRecord{s.ID: s.record()}}
			ss := newSessions(records, nil)
			if live {
				ss.put(s)
			}
			ctx, _ := sessionRequest(ss, s, secret)
			if ctx.lookupSession() != nil {
				t.Fatal("recent lookup bypassed expired session deadline")
			}
			if ss.Holds(s.ID, "one@example.org") {
				t.Fatal("expired session can still receive account notifications")
			}
			signed, err := ss.SignedIn("one@example.org", "")
			if err != nil || len(signed) != 0 {
				t.Fatalf("expired session listed as signed in: %v, %v", signed, err)
			}
		})
	}
}

func TestLocalPathRejectsBrowserAuthorityAndSchemes(t *testing.T) {
	for _, raw := range []string{
		"https://evil.example/path", "//evil.example/path",
		"http:/evil.example/path", "https:/evil.example/path", "javascript:/a/path",
		"///evil.example/path", "////evil.example/path",
		"/%2Fevil.example/path", "/%2f%2fevil.example/path", "%2F%2Fevil.example/path",
		`/\evil.example/path`, "relative/path", "",
	} {
		t.Run(raw, func(t *testing.T) {
			if got := localPath(raw); got != "" {
				t.Fatalf("accepted non-local return path %q as %q", raw, got)
			}
		})
	}
	for _, raw := range []string{"/", "/mail/INBOX", "/safe?next=//evil.example/path", "/a%20b#part"} {
		if got := localPath(raw); got != raw {
			t.Errorf("local path %q became %q", raw, got)
		}
	}
}

func TestNotificationPreviewsFollowTheRecipientSessionLock(t *testing.T) {
	s, _ := newSession()
	now := time.Now()
	if got := pushBody(s, "account", "preview", now); got != "account\npreview" {
		t.Fatalf("unprotected session: %q", got)
	}
	s.addPasskey(alborz.Passkey{Name: "test"})
	s.unlock(now)
	if got := pushBody(s, "account", "preview", now); got != "account\npreview" {
		t.Fatalf("unlocked passkey session: %q", got)
	}
	s.LockNow()
	if got := pushBody(s, "account", "preview", now); got != "account" {
		t.Fatalf("locked session leaked preview: %q", got)
	}
	s.unlock(now)
	if got := pushBody(s, "account", "preview", now.Add(s.Lock().Wait()+time.Second)); got != "account" {
		t.Fatalf("idle session leaked preview: %q", got)
	}
	if got := pushBody(nil, "account", "preview", now); got != "account" {
		t.Fatalf("unrestored session leaked preview: %q", got)
	}
}

// The profile is the person's, so All accounts reads the zone one account's page does.
func TestMergedScopeReadsThePersonsZone(t *testing.T) {
	server := rememberedServer(t, nil)
	ctx := rememberedContext(t, server)
	for _, account := range []string{"one@test.localdomain", "two@test.localdomain"} {
		login, err := server.SignIn(account, "password", "")
		if err != nil {
			t.Fatal(err)
		}
		ctx.session.add(login)
	}
	ctx.session.setProfile(alborz.Profile{Reading: alborz.Reading{Timezone: "Asia/Tehran"}})
	zone := ""
	err := Accounts(func(ctx *Context) error {
		if !ctx.Unified {
			t.Fatal("two accounts with none named are not the merged scope")
		}
		zone = UserLocation(ctx).String()
		return nil
	})(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if zone != "Asia/Tehran" {
		t.Errorf("the merged scope reads %q", zone)
	}
}

func TestFullAttachmentCacheStaysUsable(t *testing.T) {
	s, _ := newSession()
	body, err := seal.NewSpool(func(w io.Writer) error {
		_, err := w.Write(make([]byte, MaxAttachmentSize))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	big := &File{Spool: body}
	if _, err := s.putAttachment(big); err != nil {
		t.Fatal(err)
	}
	if _, err := s.putAttachment(big); err != ErrAttachmentCacheSize {
		t.Fatalf("a second full-size attachment was taken: %v", err)
	}
	done := make(chan struct{})
	go func() {
		s.popAttachment("none")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the cache stayed locked after refusing an attachment")
	}
}
