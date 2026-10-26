package web_test

import (
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// Mailman appends an inline footer outside the sender's alternative body.
func TestMailingListFooterDoesNotReplaceTheMessage(t *testing.T) {
	const body = "The Distributions DevRoom is returning to FOSDEM"
	const footer = "FOSDEM mailing list -- fosdem@lists.fosdem.org"
	raw := "From: sender@example.org\r\nTo: " + User + "\r\nSubject: Distributions Devroom\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=outer\r\n\r\n" +
		"--outer\r\nContent-Type: multipart/alternative; boundary=inner\r\n\r\n" +
		"--inner\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + " for a half-day.\r\n" +
		"--inner\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"<div dir=3D\"ltr\">" + body + " for a half-day.</div>\r\n--inner--\r\n" +
		"--outer\r\nContent-Type: text/plain; charset=us-ascii\r\nContent-Disposition: inline\r\n\r\n" + footer + "\r\n--outer--\r\n"
	a, c := SignedIn(t, User)
	uid := a.IMAP.Append(t, "INBOX", raw)
	for _, preferHTML := range []string{"", "1"} {
		c.Post(t, "settings.profile", url.Values{"prefer_html": {preferHTML}})
		page := c.Page(t, "mail.message", "INBOX", uid)
		if !strings.Contains(page, body) {
			t.Errorf("prefer_html=%q: the message body is missing", preferHTML)
		}
	}
	if page := c.Page(t, "mail.message", "INBOX", uid, url.Values{"part": {"2"}}); !strings.Contains(page, "FOSDEM mailing list") {
		t.Fatal("the separately addressed footer is no longer accessible")
	}
}
