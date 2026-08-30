package alborz_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	_ "git.mehdix.org/alborz/plugins/base"
	_ "git.mehdix.org/alborz/plugins/viewhtml"
	_ "git.mehdix.org/alborz/plugins/viewtext"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
)

const smokeUser = "a@test.local"
const smokePass = "x"

// startIMAP serves one seeded account in memory, on a port the OS picks.
func startIMAP(t *testing.T) string {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(smokeUser, smokePass)
	for _, mbox := range []struct {
		name string
		attr imap.MailboxAttr
	}{
		{"INBOX", ""}, {"Drafts", imap.MailboxAttrDrafts}, {"Sent", imap.MailboxAttrSent},
		{"Junk", imap.MailboxAttrJunk}, {"Trash", imap.MailboxAttrTrash},
		{"Archive", imap.MailboxAttrArchive},
		// A folder under another: its name carries the separator, and
		// every URL that names it has to escape it.
		{"INBOX/Lists", ""},
	} {
		var opts *imap.CreateOptions
		if mbox.attr != "" {
			opts = &imap.CreateOptions{SpecialUse: []imap.MailboxAttr{mbox.attr}}
		}
		if err := user.Create(mbox.name, opts); err != nil {
			t.Fatalf("create %s: %v", mbox.name, err)
		}
	}
	// A plain message, one from a list and one carrying an attachment:
	// the three render different toolbars and different rows, and a
	// branch nothing opens is a branch nothing checks.
	for _, m := range []struct{ from, subj, extra, body string }{
		{"Eve <eve@example.org>", "Ideas for the redesign",
			"Content-Type: text/plain; charset=UTF-8\r\n", "Body.\r\n"},
		{"Frank <frank@example.org>", "[discuss] Threading",
			"List-Id: Rig discuss <discuss.lists.example.org>\r\n" +
				"List-Post: <mailto:discuss@lists.example.org>\r\n" +
				"List-Unsubscribe: <https://lists.example.org/u>\r\n" +
				"Content-Type: text/plain; charset=UTF-8\r\n", "Body.\r\n"},
		{"Bob <bob@example.org>", "Attached docs",
			"Content-Type: multipart/mixed; boundary=b1\r\n",
			"--b1\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nBody.\r\n" +
				"--b1\r\nContent-Type: application/pdf\r\n" +
				"Content-Disposition: attachment; filename=\"doc.pdf\"\r\n\r\n%PDF-\r\n" +
				// One attachment with no filename: the row has to name
				// it something, and that branch is the one a template
				// conditional skips on every named attachment.
				"--b1\r\nContent-Type: application/octet-stream\r\n" +
				"Content-Disposition: attachment\r\n\r\nraw\r\n" +
				"--b1--\r\n"},
	} {
		raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n"+
			"Message-ID: <%d@test>\r\nMIME-Version: 1.0\r\n%s\r\n%s",
			m.from, smokeUser, m.subj, time.Now().Format(time.RFC1123Z), time.Now().UnixNano(), m.extra, m.body)
		for _, mbox := range []string{"INBOX", "INBOX/Lists"} {
			if _, err := user.Append(mbox, literal{strings.NewReader(raw), int64(len(raw))},
				&imap.AppendOptions{}); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
	}
	mem.AddUser(user)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(c *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {},
			imap.CapCondStore: {}, imap.CapSort: {}, imap.CapMetadata: {},
		},
		InsecureAuth: true,
	})
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

type literal struct {
	io.Reader
	n int64
}

func (l literal) Size() int64 { return l.n }

// startAlborz brings the app up against that IMAP server.
func startAlborz(t *testing.T, imapAddr string) string {
	t.Helper()
	key := fernet.MustDecodeKeys("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")[0]
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	_, err := alborz.New(e, &alborz.Options{
		Upstreams:  []string{"test.local=imap+insecure://" + imapAddr},
		Theme:      "alborz",
		ThemesPath: "./themes",
		LoginKey:   key,
	})
	if err != nil {
		t.Fatalf("start alborz: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go e.Server.Serve(ln)
	t.Cleanup(func() { e.Close() })
	return "http://" + ln.Addr().String()
}

// TestPagesAnswer opens every page a signed-in reader can reach and
// fails on anything the server could not render. A page that 500s is
// invisible to a compiler and to every test that does not open it,
// which is how a renamed template shipped.
func TestPagesAnswer(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}

	form := url.Values{"username": {smokeUser}, "password": {smokePass}}
	resp, err := c.PostForm(base+"/login", form)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resp.Body.Close()

	body := get(t, c, base+"/mailbox/INBOX")
	if !strings.Contains(body, "message-row") {
		t.Fatalf("inbox has no messages; the rig seeded none and every other check is vacuous")
	}
	uids := messageUIDs(body)
	if len(uids) < 3 {
		t.Fatalf("expected the three seeded messages, found %d", len(uids))
	}
	// The attachment row is the one that exercises the chips and the
	// part-less reply. A page that renders none of it grades every check
	// below it as a pass without having run them.
	if !strings.Contains(body, "attach-chip") {
		t.Fatalf("the seeded attachment renders no chip; the row it belongs to is unchecked")
	}

	// The nested folder's own rows: a part downloaded from there is the
	// one URL that has to escape the separator in the mailbox name.
	nested := get(t, c, base+"/mailbox/INBOX%2FLists")
	for _, href := range rawPartHrefs(nested) {
		t.Run(href, func(t *testing.T) { get(t, c, base+href) })
	}
	if len(rawPartHrefs(nested)) == 0 {
		t.Fatal("the nested folder shows no downloadable part; the escaping is unchecked")
	}

	paths := []string{
		"/mailbox/INBOX", "/mailbox/INBOX?starred=1", "/mailbox/INBOX?query=redesign",
		"/mailbox/INBOX%2FLists",
		"/mailbox/Drafts", "/mailbox/Junk", "/mailbox/Trash",
		"/compose", "/new-mailbox", "/settings", "/settings/browser",
	}
	for _, uid := range uids {
		paths = append(paths,
			"/message/INBOX/"+uid,
			"/message/INBOX/"+uid+"/reply",
			"/message/INBOX/"+uid+"/forward",
			"/message/INBOX/forward?uids="+uid,
		)
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) { get(t, c, base+p) })
	}
}

// rawPartHrefs finds the download links a list row offers, as written
// in the page - the escaping under test is the page's, not the test's.
func rawPartHrefs(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(
		`href="(/message/[^"]*/raw\?[^"]*)"`).FindAllStringSubmatch(body, -1) {
		out = append(out, strings.ReplaceAll(m[1], "&amp;", "&"))
	}
	return out
}

func get(t *testing.T, c *http.Client, u string) string {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	if resp.StatusCode >= 500 {
		t.Fatalf("GET %s: %s\n%s", u, resp.Status, firstLines(buf.String()))
	}
	if resp.StatusCode >= 400 {
		t.Fatalf("GET %s: %s", u, resp.Status)
	}
	return buf.String()
}

func messageUIDs(body string) []string {
	var out []string
	for _, part := range strings.Split(body, `name="uids" value="`)[1:] {
		if i := strings.IndexByte(part, '"'); i > 0 {
			uid := part[:i]
			if len(out) == 0 || out[len(out)-1] != uid {
				out = append(out, uid)
			}
		}
	}
	return out
}

func firstLines(s string) string {
	lines := strings.SplitN(s, "\n", 6)
	if len(lines) > 5 {
		lines = lines[:5]
	}
	return strings.Join(lines, "\n")
}
