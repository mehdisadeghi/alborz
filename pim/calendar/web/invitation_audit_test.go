package web_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestCancellationDoesNotSelectASubstringUID(t *testing.T) {
	a, c := SignedIn(t, User)
	coll := c.CreateCalendar(t, "Identity", "")
	before := strings.Replace(Event("prefix-event-123", "Keep me"), "BEGIN:VEVENT\r\n", "BEGIN:VEVENT\r\nORGANIZER:mailto:b@example.org\r\n", 1)
	a.Put(t, coll+"keep.ics", "text/calendar", before)
	msg := "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:test\nMETHOD:CANCEL\nBEGIN:VEVENT\nUID:event-123\nORGANIZER:mailto:b@example.org\nSEQUENCE:1\nDTSTAMP:20260902T100000Z\nEND:VEVENT\nEND:VCALENDAR\n"
	uid := a.IMAP.Append(t, "INBOX", calendarMail("b@example.org", "Cancelled", "CANCEL", msg))
	page := c.Page(t, "mail.message", "INBOX", uid)
	if strings.Contains(page, `action="`+a.Path("calendar.frommessage")+`"`) {
		t.Error("substring UID offered as filed")
	}
	c.Post(t, "calendar.frommessage", url.Values{"mbox": {"INBOX"}, "uid": {fmt.Sprint(uid)}})
	got := a.DAV(t, http.MethodGet, coll+"keep.ics", nil, "")
	if got.StatusCode != 200 || got.Text != before {
		t.Fatalf("unrelated event changed: %s %s", got.Status, got.Text)
	}
}

func TestInvitationRefusesAConcurrentEdit(t *testing.T) {
	for _, method := range []string{"REQUEST", "CANCEL", "PUBLISH"} {
		t.Run(method, func(t *testing.T) {
			coll := DAVHome + "identity/"
			at := coll + "event-123.ics"
			before := strings.Replace(Event("event-123", "Original"), "BEGIN:VEVENT\r\n", "BEGIN:VEVENT\r\nORGANIZER:mailto:b@example.org\r\n", 1)
			opts := []DAVOption{Calendar(coll, "Identity")}
			if method != "PUBLISH" {
				opts = append(opts, Object(at, before))
			}
			d := DAV(t, opts...)
			var attempts atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PUT" || r.Method == "DELETE" {
					attempts.Add(1)
					safe := r.Header.Get("If-Match") == `"1"`
					if method == "PUBLISH" {
						safe = r.Header.Get("If-None-Match") == "*"
					}
					if safe {
						http.Error(w, "changed by another client", 412)
						return
					}
				}
				d.ServeHTTP(w, r)
			}))
			defer proxy.Close()
			a, c := SignedIn(t, User, Upstream(Domain, "caldav+insecure://"+strings.TrimPrefix(proxy.URL, "http://")+"/dav"), NoCache())
			msg := "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:test\nMETHOD:" + method + "\nBEGIN:VEVENT\nUID:event-123\nORGANIZER:mailto:b@example.org\nSEQUENCE:1\nDTSTAMP:20260902T100000Z\nDTSTART:20260915T100000Z\nDTEND:20260915T110000Z\nSUMMARY:Incoming\nEND:VEVENT\nEND:VCALENDAR\n"
			uid := a.IMAP.Append(t, "INBOX", calendarMail("b@example.org", "Incoming", method, msg))
			resp := c.Post(t, "calendar.frommessage", url.Values{"mbox": {"INBOX"}, "uid": {fmt.Sprint(uid)}, "calendar": {User + "|" + At(proxy.URL) + coll}})
			if attempts.Load() != 1 {
				t.Fatalf("mutation attempts: %d (%s)", attempts.Load(), resp.Status)
			}
			bar := NoticeBar(c.Follow(t, resp.Header.Get("Location")))
			if !strings.Contains(bar, "notice-failed") || !strings.Contains(bar, "412") {
				t.Errorf("no concurrent-edit refusal: %s", bar)
			}
			if len(d.Puts()) != 0 {
				t.Errorf("incoming message overwrote concurrent edit: %v", d.Puts())
			}
		})
	}
}
