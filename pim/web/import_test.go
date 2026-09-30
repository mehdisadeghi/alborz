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
	const refused = "The change was not saved. The server said:"
	personal, work, friends, family := DAVHome+"personal/", DAVHome+"work/", DAVBooks+"friends/", DAVBooks+"family/"
	card := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:ada\r\nFN:Ada\r\nEND:VCARD\r\n"
	d := DAV(t, Calendar(personal, "Personal"), Calendar(work, "Work"), Hangup(work), Book(friends, "Friends"), Book(family, "Family"),
		Refusing(personal, friends, family+"bob.vcf"))
	a := Alborz(t, NoCache(), WithDAV(d))
	c := a.SignIn(t, User)
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
			if page := c.Follow(t, resp.Header.Get("Location")); !strings.Contains(page, refused) || !strings.Contains(page, "read-only collection") {
				t.Errorf("the page after the refused import does not give the server's reason")
			}
		})
	}
	const unreadable = "The file could not be read:"
	for _, tc := range []struct{ name, ics, reason string }{
		{"an unreadable file", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:plan\r\nSUMMARY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n", "missing colon"},
		{"a parameter the line ends in", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:plan\r\nX;A=b\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n", ""},
		{"a file cut off inside an event", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:plan\r\n", ""},
	} {
		t.Run(tc.name+" on the import page", func(t *testing.T) {
			resp := c.Upload(t, "calendar.import", url.Values{"collection": {User + "|" + personal}}, file("plan.ics", tc.ics))
			if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(resp.Text, unreadable) || !strings.Contains(resp.Text, tc.reason) {
				t.Errorf("the file answered %s without saying it could not be read, and why; want 422", resp.Status)
			}
		})
	}
	t.Run("an unreadable file into an address book", func(t *testing.T) {
		resp := c.Upload(t, "contacts.book.import", nil, file("ada.vcf", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ada\r\n"), friends)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("the unreadable file answered %s, want a redirect", resp.Status)
		}
		if page := c.Follow(t, resp.Header.Get("Location")); !strings.Contains(page, unreadable) || !strings.Contains(page, "no END field") {
			t.Errorf("the page after the unreadable file does not say it could not be read, and why")
		}
	})
	t.Run("a file the server refuses partway", func(t *testing.T) {
		resp := c.Upload(t, "contacts.book.import", nil, file("family.vcf", card+"BEGIN:VCARD\r\nVERSION:4.0\r\nUID:bob\r\nFN:Bob\r\nEND:VCARD\r\n"), family)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("the partly refused import answered %s, want a redirect", resp.Status)
		}
		if page := c.Follow(t, resp.Header.Get("Location")); !strings.Contains(page, "1 of 2 saved; the server refused the rest:") || !strings.Contains(page, "read-only collection") {
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
	t.Run("a server that does not answer the import page", func(t *testing.T) {
		resp := c.Upload(t, "calendar.import", url.Values{"collection": {User + "|" + work}}, file("plan.ics", Event("plan", "Plan")))
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("the import no server answered gave %s, want 500", resp.Status)
		}
	})
}
