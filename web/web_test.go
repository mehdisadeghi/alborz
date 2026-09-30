package web_test

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
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

// A part keeps the length the server knows, and an image is compressed already.
func TestDownloadsSentAsTheyAre(t *testing.T) {
	a := Alborz(t)
	text := strings.Repeat("A line of a long attachment.\r\n", 2000)
	uid := a.IMAP.Append(t, "INBOX", "From: <eve@example.org>\r\nTo: <"+User+">\r\nSubject: Attached\r\n"+
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b1\r\n\r\n"+
		"--b1\r\nContent-Type: text/plain\r\n\r\nSee attached.\r\n"+
		"--b1\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=\"notes.txt\"\r\n\r\n"+text+"\r\n--b1--\r\n")
	c := a.SignIn(t, User)
	for _, tc := range []struct{ name, path string }{
		{"a part", a.Path("mail.message.raw", "INBOX", uid) + "?part=2"},
		{"an icon", a.Path("core.asset", "icon-512.png")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, a.URL+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Accept-Encoding", "gzip")
			resp := c.Do(t, req)
			if resp.StatusCode != http.StatusOK || len(resp.Text) < 1<<10 {
				t.Fatalf("download: %s, %d bytes", resp.Status, len(resp.Text))
			}
			if ce := resp.Header.Get("Content-Encoding"); ce != "" {
				t.Errorf("the download was compressed: Content-Encoding %q", ce)
			}
			if resp.Header.Get("Content-Length") != fmt.Sprint(len(resp.Text)) {
				t.Errorf("the download's Content-Length %q is not its %d bytes", resp.Header.Get("Content-Length"), len(resp.Text))
			}
			if len(resp.Header.Values("No-Gzip-Compression")) != 0 {
				t.Errorf("the compressor's own header reached the browser")
			}
		})
	}
	if got := c.Follow(t, a.Path("mail.message.raw", "INBOX", uid)+"?part=2"); got != text {
		t.Errorf("the part is %d bytes, not the %d-byte attachment", len(got), len(text))
	}
}

// An installed app opens its home, and its worker serves every page under the scope.
func TestManifestScopesTheApp(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	var manifest struct {
		StartURL string `json:"start_url"`
		Scope    string
	}
	if err := json.Unmarshal([]byte(c.Get(t, "core.manifest").Text), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.StartURL != a.Path("mail.home") {
		t.Errorf("the manifest starts at %q, not the home page", manifest.StartURL)
	}
	if worker := a.Path("core.worker"); !strings.HasSuffix(manifest.Scope, "/") || !strings.HasPrefix(worker, manifest.Scope) || strings.Contains(worker[len(manifest.Scope):], "/") {
		t.Errorf("the manifest's scope %q is not the directory of the worker at %q", manifest.Scope, worker)
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
