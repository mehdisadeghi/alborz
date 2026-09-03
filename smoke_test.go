package alborz_test

import (
	"bytes"
	"fmt"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"html"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
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

// sentMail collects what the server actually put on the wire. The
// message a reader receives is the only place some decisions are
// visible - whether an HTML part was added, for one - and asserting on
// the compose form instead is how that went unnoticed.
type sentMail struct {
	mu   sync.Mutex
	last string
}

func (s *sentMail) Last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

type smtpBackend struct{ got *sentMail }

func (b *smtpBackend) NewSession(_ *smtp.Conn) (smtp.Session, error) {
	return &smtpSession{got: b.got}, nil
}

type smtpSession struct{ got *sentMail }

// The client authenticates before it will send, so the sink has to
// offer a mechanism. Any credentials pass: this stands in for a server,
// it does not test one.
func (s *smtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *smtpSession) Auth(mech string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(identity, username, password string) error {
		return nil
	}), nil
}

func (s *smtpSession) Mail(string, *smtp.MailOptions) error { return nil }
func (s *smtpSession) Rcpt(string, *smtp.RcptOptions) error { return nil }
func (s *smtpSession) Reset()                               {}
func (s *smtpSession) Logout() error                        { return nil }

func (s *smtpSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.got.mu.Lock()
	s.got.last = string(b)
	s.got.mu.Unlock()
	return nil
}

// startSMTP accepts everything and remembers the last message.
func startSMTP(t *testing.T) (string, *sentMail) {
	t.Helper()
	got := &sentMail{}
	srv := smtp.NewServer(&smtpBackend{got: got})
	srv.AllowInsecureAuth = true
	srv.Domain = "localhost"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String(), got
}

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
		// HTML with an inline image: the sanitizer rewrites cid: to the
		// part's own raw URL, which is the one place that URL is built
		// - and in a folder whose name holds a separator it has to
		// escape it or the picture 404s.
		{"Hal <hal@example.org>", "Inline picture",
			"Content-Type: multipart/related; boundary=r1\r\n",
			"--r1\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n" +
				"<p>See <img src=\"cid:pic@rig\"></p>\r\n" +
				"--r1\r\nContent-Type: image/png\r\nContent-ID: <pic@rig>\r\n\r\nPNG\r\n" +
				"--r1--\r\n"},
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
func startAlborz(t *testing.T, imapAddr string, smtpAddr ...string) string {
	t.Helper()
	key := fernet.MustDecodeKeys("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")[0]
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	_, err := alborz.New(e, &alborz.Options{
		Upstreams: append([]string{"test.local=imap+insecure://" + imapAddr},
			smtpUpstreams(smtpAddr)...),
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
	if len(uids) < 4 {
		t.Fatalf("expected the four seeded messages, found %d", len(uids))
	}
	// The attachment row is the one that exercises the part-less reply
	// and the download links. A page that renders none of it grades
	// every check below it as a pass without having run them.
	if !strings.Contains(body, "mailbox.attachment") && !strings.Contains(body, "message-mark") {
		t.Fatalf("the seeded attachment leaves no mark on its row; that row is unchecked")
	}

	// A part downloaded out of a folder whose name holds the hierarchy
	// separator is the one URL that has to escape it. The links are on
	// the message page, so the message has to be opened to reach them.
	nested := get(t, c, base+"/mailbox/INBOX%2FLists")
	nestedUIDs := messageUIDs(nested)
	if len(nestedUIDs) == 0 {
		t.Fatal("the nested folder shows no messages; the escaping is unchecked")
	}
	var nestedParts []string
	for _, uid := range nestedUIDs {
		page := get(t, c, base+"/message/INBOX%2FLists/"+uid)
		nestedParts = append(nestedParts, rawPartHrefs(page)...)
		// The sanitizer rewrites an inline cid: image to the part's own
		// raw URL; that is the only place alborz builds one, so it is
		// where the mailbox name's escaping is proved.
		nestedParts = append(nestedParts, rawPartSrcs(page)...)
	}
	if len(nestedParts) == 0 {
		t.Fatal("no downloadable part in the nested folder; the escaping is unchecked")
	}
	for _, href := range nestedParts {
		t.Run(href, func(t *testing.T) { get(t, c, base+href) })
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

// rawPartSrcs finds the part URLs the HTML sanitizer wrote into a
// rendered message - the inline images, whose src it rewrote. The
// sanitized document is carried in an iframe's srcdoc, so it arrives
// attribute-escaped and has to be unescaped before it reads as HTML.
func rawPartSrcs(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(
		`src="(/message/[^"]*/raw\?[^"]*)"`).FindAllStringSubmatch(
		html.UnescapeString(body), -1) {
		out = append(out, m[1])
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

func login(t *testing.T, base string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	resp, err := c.PostForm(base+"/login",
		url.Values{"username": {smokeUser}, "password": {smokePass}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resp.Body.Close()
	return c
}

// TestUnknownSearchKeySurvives covers a crash that cost more than the
// page it was on. PrepareSearch skips a term it does not recognise, so a
// query made only of those produced nil criteria and sortSeqNums
// dereferenced it. Worse than the 500: the panic skipped the watchdog
// that guards the session's IMAP connection, which was then closed but
// still held, so every later request on that session waited out the
// round-trip timeout. Hence the second half of this test - answering the
// bad query is not enough, the session has to still work afterwards.
func TestUnknownSearchKeySurvives(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)

	if body := get(t, c, base+"/mailbox/INBOX?query=foo:bar"); body == "" {
		t.Fatal("the unrecognised key produced no page")
	}
	if body := get(t, c, base+"/mailbox/INBOX"); !strings.Contains(body, "message-row") {
		t.Fatal("the session stopped serving mail after an unrecognised search key")
	}
}

// TestMailtoHandlerRefusesForeignURI guards a redirect. The browser is
// invited to send mailto: links to /compose?mailto=%s, and
// composeFromMailto hands back anything that is not a mailto URI
// unchanged - so redirecting to whatever it returns would forward the
// reader to any address an attacker put in the link.
func TestMailtoHandlerRefusesForeignURI(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	for _, uri := range []string{"https://evil.example/x", "//evil.example/x", "javascript:alert(1)"} {
		resp, err := c.Get(base + "/compose?mailto=" + url.QueryEscape(uri))
		if err != nil {
			t.Fatalf("GET mailto=%s: %v", uri, err)
		}
		resp.Body.Close()
		if loc := resp.Header.Get("Location"); loc != "" {
			t.Fatalf("mailto=%s redirected to %q", uri, loc)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("mailto=%s answered %s, want 400", uri, resp.Status)
		}
	}

	// The handler still has to do its job, or the check above passes by
	// refusing everything.
	resp, err := c.Get(base + "/compose?mailto=" + url.QueryEscape("mailto:a@b.example?subject=Hi"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/compose?") || !strings.Contains(loc, "to=a@b.example") ||
		!strings.Contains(loc, "subject=Hi") {
		t.Fatalf("a real mailto URI redirected to %q", loc)
	}
}

// TestExportFailsAsAnError is about what a failure looks like from the
// outside. The handler used to write the mbox headers and a 200 before
// fetching anything, so a fetch that failed had the HTML error page
// rendered into a body already claiming to be an mbox - the reader
// saved a .mbox file containing "500: Internal Server Error".
func TestExportFailsAsAnError(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)

	body := get(t, c, base+"/mailbox/INBOX")
	uids := messageUIDs(body)
	if len(uids) < 2 {
		t.Fatalf("need two seeded messages to export, found %d", len(uids))
	}

	// A message that is not there is the failure this reproduces.
	resp, err := c.PostForm(base+"/message/INBOX/export",
		url.Values{"uids": {"999999"}})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	resp.Body.Close()

	if resp.StatusCode < 400 {
		t.Errorf("a missing message exported with status %s", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/mbox") {
		t.Errorf("an error was served as %s", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		t.Errorf("an error was offered as a download: %q", cd)
	}

	// And the working case still produces an mbox, or the checks above
	// pass by refusing everything.
	resp, err = c.PostForm(base+"/message/INBOX/export", url.Values{"uids": uids[:2]})
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	buf.ReadFrom(resp.Body)
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/mbox") {
		t.Fatalf("a good export was served as %q", ct)
	}
	if n := strings.Count(buf.String(), "\r\nFrom ") + strings.Count(
		buf.String()[:min(6, buf.Len())], "From "); n < 2 {
		t.Errorf("expected two messages, found %d separators", n)
	}
	if strings.Contains(buf.String(), "<!DOCTYPE") {
		t.Error("the export carries an HTML page inside it")
	}
}

// TestSendHTMLIsDecidedByTheAccount is about where a decision lives.
// The HTML part used to be chosen when the compose page was rendered and
// carried back in a hidden field, so a page opened before the setting
// was turned on sent no direction at all - and the form looked right
// the whole time. The account's setting decides at send now, and a
// reply to a list is refused by asking that message, not the page.
//
// It asserts the bytes that went to the SMTP server, because that is
// the only place the answer is visible.
func TestSendHTMLIsDecidedByTheAccount(t *testing.T) {
	smtpAddr, sent := startSMTP(t)
	base := startAlborz(t, startIMAP(t), smtpAddr)
	c := login(t, base)

	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	var listUID string
	for _, uid := range uids {
		if strings.Contains(get(t, c, base+"/message/INBOX/"+uid+"?part=1"), "Mailing list") {
			listUID = uid
			break
		}
	}
	if listUID == "" {
		t.Fatal("the seeded list message is missing; the list case would go unchecked")
	}

	set := func(on bool) {
		form := url.Values{"messages_per_page": {"50"}}
		if on {
			form.Set("send_html", "1")
		}
		resp, err := c.PostForm(base+"/settings", form)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	// The page is fetched once, before the setting is touched. Under the
	// old design everything sent from it was plain whatever came later.
	stale := get(t, c, base+"/compose")

	set(true)
	sendFrom(t, c, base, "/compose", stale)
	if !strings.Contains(sent.Last(), "multipart/alternative") {
		t.Errorf("a page rendered before the setting sent no HTML part:\n%s", headOf(sent.Last()))
	}

	set(false)
	sendFrom(t, c, base, "/compose", get(t, c, base+"/compose"))
	if strings.Contains(sent.Last(), "multipart/alternative") {
		t.Errorf("an HTML part was sent though the account did not ask:\n%s", headOf(sent.Last()))
	}

	// A reply to a list refuses it however the account is set, and that
	// is read from the message rather than from the form.
	set(true)
	path := "/message/INBOX/" + listUID + "/reply?part=1"
	sendFrom(t, c, base, path, get(t, c, base+path))
	if strings.Contains(sent.Last(), "multipart/alternative") {
		t.Errorf("a reply to list mail carried an HTML part:\n%s", headOf(sent.Last()))
	}
}

// sendFrom submits the compose form exactly as the page presented it.
func sendFrom(t *testing.T, c *http.Client, base, path, page string) {
	t.Helper()
	mid := strings.NewReplacer("&lt;", "<", "&gt;", ">").Replace(
		between(page, `name="message_id" value="`, `"`))
	if mid == "" {
		t.Fatalf("%s carries no message id", path)
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, f := range [][2]string{
		{"message_id", mid},
		{"in_reply_to", between(page, `name="in_reply_to" value="`, `"`)},
		{"from", smokeUser}, {"to", "friend@example.org"},
		{"subject", "direction"}, {"text", "سلام\n\nEnglish."},
	} {
		if err := w.WriteField(f[0], f[1]); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	resp, err := c.Post(base+path, w.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("POST %s: %s", path, resp.Status)
	}
}

func headOf(s string) string {
	if i := strings.Index(s, "\r\n\r\n"); i > 0 {
		return s[:i]
	}
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

// sendAndReadSent saves a draft and hands back its raw bytes. The rig
// has no SMTP, but a draft is written by the same WriteMessage a send
// uses, so it answers the only question here: what the message is
// actually made of.
func sendAndReadSent(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	page := get(t, c, base+"/compose")
	mid := between(page, `name="message_id" value="`, `"`)
	if mid == "" {
		t.Fatal("compose page carries no message id")
	}
	mid = strings.ReplaceAll(strings.ReplaceAll(mid, "&lt;", "<"), "&gt;", ">")

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, f := range [][2]string{
		{"message_id", mid}, {"from", smokeUser}, {"to", "friend@example.org"},
		{"subject", "direction"}, {"text", "سلام\n\nEnglish."}, {"save_as_draft", "1"},
	} {
		if err := w.WriteField(f[0], f[1]); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	resp, err := c.Post(base+"/compose", w.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	drafts := get(t, c, base+"/mailbox/Drafts")
	uids := messageUIDs(drafts)
	if len(uids) == 0 {
		t.Fatal("the draft was not saved; nothing to inspect")
	}
	return get(t, c, base+"/message/Drafts/"+uids[0]+"/raw")
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func smtpUpstreams(addrs []string) []string {
	var out []string
	for _, a := range addrs {
		out = append(out, "test.local=smtp+insecure://"+a)
	}
	return out
}
