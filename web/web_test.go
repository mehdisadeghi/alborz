package web_test

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// A compressor that miscounts the write crossing its threshold breaks a
// download that reaches it in small writes.
func TestGzippedDownloadArrivesWhole(t *testing.T) {
	// Trickled, the message reaches the response in writes below the threshold.
	a := Alborz(t, WithIMAP(IMAP(t, Trickle(512))))
	raw := "From: <eve@example.org>\r\nTo: <" + User + ">\r\nSubject: Gzipped\r\n\r\n" +
		strings.Repeat("A line of a long message.\r\n", 2000)
	uid := a.IMAP.Append(t, "INBOX", raw)

	c := a.SignIn(t, User)
	req, err := http.NewRequest(http.MethodGet, a.URL+a.Path("mail.message.eml", "INBOX", uid), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Set by hand, the header turns off the transport's own decoding.
	req.Header.Set("Accept-Encoding", "gzip")
	resp := c.Do(t, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download: %s", resp.Status)
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("the download was not compressed: Content-Encoding %q", ce)
	}
	zr, err := gzip.NewReader(strings.NewReader(resp.Text))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("reading the download after %d bytes: %v", len(got), err)
	}
	if string(got) != raw {
		t.Errorf("the download is %d bytes, not the %d-byte message sent", len(got), len(raw))
	}
}

func TestDoneNoticeClosesWithoutAScript(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	c.Post(t, "mail.messages.move", url.Values{"uids": uids[:1]}, "INBOX", url.Values{"to": {"Trash"}})
	notice := NoticeBar(c.Page(t, "mail.mailbox", "INBOX"))
	if !strings.Contains(notice, "notice-done") {
		t.Fatalf("no done notice after a move: %q", notice)
	}
	if !strings.Contains(notice, `<form method="dialog" class="notice-dismiss">`) {
		t.Errorf("the notice has no cross that closes it without a script:\n%s", notice)
	}
}

// The page depends on the error keeping its type through every wrap.
func TestUpstreamOutageIsNotOurFailure(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}

	// The reader's goroutine notices the loss a moment after Close returns.
	a.IMAP.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := c.Post(t, "mail.messages.flag", url.Values{"uids": {uids[0]}, "flags": {"\\Seen"}}, "INBOX")
		if resp.StatusCode == http.StatusBadGateway {
			if !strings.Contains(resp.Text, "did not answer") && !strings.Contains(resp.Text, "could not reach") {
				t.Errorf("502 without the upstream page:\n%s", resp.Text[:min(500, len(resp.Text))])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("an unreachable server was answered with %s:\n%s", resp.Status, resp.Text[:min(500, len(resp.Text))])
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A notice filed under an account would turn up later on something unrelated.
func TestNoticeReachesTheReaderFromAnyScope(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	scoped := url.Values{"account": {User2}}
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", scoped))
	if len(uids) == 0 {
		t.Fatal("the second account has no message to act on")
	}
	c.Post(t, "mail.messages.move", url.Values{"uids": {uids[0]}, "to": {"Archive"}, "next": {a.Path("mail.mailbox", "INBOX")}}, "INBOX", scoped)
	if !strings.Contains(c.Page(t, "mail.mailbox", "INBOX"), "notice-text") {
		t.Error("the merged view did not carry the notice the action raised")
	}
}

// The session id is shown to the account's other browsers, so it alone must open nothing.
func TestASessionOpensOnlyToItsOwnSecret(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	cookie := c.Cookie("alborz_session")
	if cookie == nil {
		t.Fatal("signing in left no session cookie")
	}
	id, _, _ := strings.Cut(cookie.Value, ".")
	if uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX")); len(uids) == 0 {
		t.Fatal("the session's own browser was not shown its inbox")
	}

	req, _ := http.NewRequest(http.MethodGet, a.URL+a.Path("mail.mailbox", "INBOX"), nil)
	req.AddCookie(&http.Cookie{Name: "alborz_session", Value: id + ".x"})
	if resp := a.Client().Do(t, req); resp.StatusCode == http.StatusOK {
		t.Fatal("the inbox opened to a cookie holding the id and another secret")
	}
}
