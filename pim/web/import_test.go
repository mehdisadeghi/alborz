package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// A server refusing the write is an answer for the reader, not an error page.
func TestAnImportTheServerRefusesSetsANotice(t *testing.T) {
	personal, work, friends, family := DAVHome+"personal/", DAVHome+"work/", DAVBooks+"friends/", DAVBooks+"family/"
	card := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:ada\r\nFN:Ada\r\nEND:VCARD\r\n"
	d := DAV(t, Calendar(personal, "Personal"), Calendar(work, "Work"), Hangup(work), Book(friends, "Friends"), Book(family, "Family"),
		Refusing(personal, friends, family+"bob.vcf"))
	a, c := SignedIn(t, User, NoCache(), WithDAV(d))
	personal, work, friends, family = d.At()+personal, d.At()+work, d.At()+friends, d.At()+family
	attached := func(mime, name, body string) string {
		return fmt.Sprint(a.IMAP.Append(t, "INBOX", "From: eve@example.org\r\nTo: "+User+"\r\nSubject: "+name+"\r\n"+
			"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b1\r\n\r\n"+
			"--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nAttached.\r\n"+
			"--b1\r\nContent-Type: "+mime+"; charset=utf-8\r\nContent-Disposition: attachment; filename=\""+name+"\"\r\n\r\n"+
			body+"--b1--\r\n"))
	}
	file := func(name, body string) []File {
		return []File{{Field: "file", Name: name, Body: strings.NewReader(body)}}
	}
	for _, tc := range []struct {
		name string
		post func() *Response
	}{
		{"a file into a calendar", func() *Response {
			return c.Upload(t, "calendar.collection.import", nil, file("plan.ics", Event("plan", "Plan")), personal)
		}},
		{"a file into an address book", func() *Response {
			return c.Upload(t, "contacts.book.import", nil, file("ada.vcf", card), friends)
		}},
		{"a calendar attachment", func() *Response {
			uid := attached("text/calendar", "plan.ics", Event("plan", "Plan"))
			return c.Post(t, "calendar.fromattachment", url.Values{"mbox": {"INBOX"}, "uid": {uid}, "part": {"2"}, "calendar": {User + "|" + personal}})
		}},
		{"a card attachment", func() *Response {
			uid := attached("text/vcard", "ada.vcf", card)
			return c.Post(t, "contacts.frommessage", url.Values{"mbox": {"INBOX"}, "uid": {uid}, "part": {"2"}, "addressbook": {User + "|" + friends}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.post()
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("the refused import answered %s, want a redirect", resp.Status)
			}
			if page := c.Follow(t, resp.Header.Get("Location")); !Says(t, page, "form.saverefused", Any) || !strings.Contains(page, "read-only collection") {
				t.Errorf("the page after the refused import does not give the server's reason")
			}
		})
	}
	for _, tc := range []struct{ name, ics, reason string }{
		{"an unreadable file", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:plan\r\nSUMMARY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n", "missing colon"},
		{"a parameter the line ends in", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:plan\r\nX;A=b\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n", ""},
		{"a file cut off inside an event", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:plan\r\n", ""},
	} {
		t.Run(tc.name+" on the import page", func(t *testing.T) {
			resp := c.Upload(t, "calendar.import", url.Values{"collection": {User + "|" + personal}}, file("plan.ics", tc.ics))
			if resp.StatusCode != http.StatusUnprocessableEntity || !Says(t, resp.Text, "import.unreadable", Any) || !strings.Contains(resp.Text, tc.reason) {
				t.Errorf("the file answered %s without saying it could not be read, and why; want 422", resp.Status)
			}
		})
	}
	t.Run("an unreadable file into an address book", func(t *testing.T) {
		resp := c.Upload(t, "contacts.book.import", nil, file("ada.vcf", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ada\r\n"), friends)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("the unreadable file answered %s, want a redirect", resp.Status)
		}
		if page := c.Follow(t, resp.Header.Get("Location")); !Says(t, page, "import.unreadable", Any) || !strings.Contains(page, "no END field") {
			t.Errorf("the page after the unreadable file does not say it could not be read, and why")
		}
	})
	t.Run("a file the server refuses partway", func(t *testing.T) {
		resp := c.Upload(t, "contacts.book.import", nil, file("family.vcf", card+"BEGIN:VCARD\r\nVERSION:4.0\r\nUID:bob\r\nFN:Bob\r\nEND:VCARD\r\n"), family)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("the partly refused import answered %s, want a redirect", resp.Status)
		}
		if page := c.Follow(t, resp.Header.Get("Location")); !Says(t, page, "import.partly", 1, 2, Any) || !strings.Contains(page, "read-only collection") {
			t.Errorf("the page after the partly refused import does not say how much was saved, and why the rest was not")
		}
		if !strings.Contains(strings.Join(d.Puts(), ""), "UID:ada") {
			t.Errorf("the card before the refusal was not written")
		}
	})
	t.Run("a file over the limit on the import page", func(t *testing.T) {
		resp := c.Upload(t, "calendar.import", url.Values{"collection": {User + "|" + personal}}, file("big.ics", strings.Repeat("x", 16<<20+1)))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("the file over the limit answered %s, want 422", resp.Status)
		}
		if !strings.Contains(resp.Text, "larger than 16 MB") {
			t.Errorf("the import page does not state the limit")
		}
	})
	t.Run("an attachment over the limit", func(t *testing.T) {
		uid := attached("text/calendar", "big.ics", strings.Repeat("x\r\n", 16<<20/3+2))
		resp := c.Post(t, "calendar.fromattachment", url.Values{"mbox": {"INBOX"}, "uid": {uid}, "part": {"2"}, "calendar": {User + "|" + personal}})
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("the attachment over the limit answered %s, want a redirect", resp.Status)
		}
		if page := c.Follow(t, resp.Header.Get("Location")); !strings.Contains(page, "larger than 16 MB") {
			t.Errorf("the page after the attachment over the limit does not state the limit")
		}
	})
	t.Run("a server that does not answer the import page", func(t *testing.T) {
		resp := c.Upload(t, "calendar.import", url.Values{"collection": {User + "|" + work}}, file("plan.ics", Event("plan", "Plan")))
		if resp.StatusCode != http.StatusBadGateway || !Says(t, resp.Text, "upstream.unreachable", T(t, "upstream.dav")) || !Says(t, Between(resp.Text, "<h1>", "</h1>"), "upstream.title", T(t, "upstream.dav")) {
			t.Errorf("the import no server answered gave %s without naming the server, want 502", resp.Status)
		}
	})
}

// An event without DTSTAMP is stamped as a feed's is; one go-ical will not write is left out with its
// reason, and the rest of the file is saved.
func TestAnImportSkipsTheEventItCannotWrite(t *testing.T) {
	personal := DAVHome + "personal/"
	d := DAV(t, Calendar(personal, "Personal"))
	_, c := SignedIn(t, User, NoCache(), WithDAV(d))
	personal = d.At() + personal
	event := func(uid, props string) string {
		return "BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTART:20260915T100000Z\r\n" + props + "END:VEVENT\r\n"
	}
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n" +
		event("unstamped", "SUMMARY:Unstamped\r\n") +
		event("twice", "DTSTAMP:20260901T080000Z\r\nSUMMARY:One\r\nSUMMARY:Two\r\n") +
		event("plain", "DTSTAMP:20260901T080000Z\r\nSUMMARY:Plain\r\n") +
		"END:VCALENDAR\r\n"
	resp := c.Upload(t, "calendar.collection.import", nil, []File{{Field: "file", Name: "plan.ics", Body: strings.NewReader(ics)}}, personal)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("the import answered %s, want a redirect", resp.Status)
	}
	if page := c.Follow(t, resp.Header.Get("Location")); !Says(t, page, "import.partly", 2, 3, "twice:"+Any) || !strings.Contains(page, "SUMMARY") {
		t.Errorf("the page after the import does not say which event was left out, and why")
	}
	puts := strings.Join(d.Puts(), "")
	for _, want := range []string{"UID:unstamped", "DTSTAMP:19700101T000000Z", "UID:plain"} {
		if !strings.Contains(puts, want) {
			t.Errorf("the saved events lack %s", want)
		}
	}
	if strings.Contains(puts, "UID:twice") {
		t.Error("the event go-ical refused was written")
	}
}
