package alborz_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	_ "git.mehdix.org/alborz/plugins/base"
	_ "git.mehdix.org/alborz/plugins/viewhtml"
	_ "git.mehdix.org/alborz/plugins/viewtext"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
)

const smokeUser = "a@test.local"
const smokeUser2 = "b@test.local"
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

// A recipient at refused@ is turned away the way a real submission
// server turns away an unknown user; every other one is taken.
func (s *smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	if strings.HasPrefix(to, "refused@") {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "Recipient address rejected: User unknown"}
	}
	return nil
}
func (s *smtpSession) Reset()        {}
func (s *smtpSession) Logout() error { return nil }

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

// startIMAP serves two accounts in memory, on a port the OS picks: one
// seeded with mail, and a second whose Drafts holds one message so a
// draft deleted on the wrong account is a draft that goes missing.
func startIMAP(t *testing.T) string {
	t.Helper()
	addr, _ := startIMAPServer(t, rigCaps())
	return addr
}

// rigCaps is what the in-memory server advertises by default. A test
// that takes a capability away runs the branch that a server without
// it takes. METADATA is not listed: go-imap's server does not implement
// it, and advertising it made every test run under the transient store
// while the log claimed otherwise.
func rigCaps() imap.CapSet {
	return imap.CapSet{
		imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {},
		imap.CapCondStore: {}, imap.CapSort: {},
	}
}

// startIMAPServer also hands back the server, for a test that takes it
// away again.
func startIMAPServer(t *testing.T, caps imap.CapSet) (string, *imapserver.Server) {
	t.Helper()
	mem := imapmemserver.New()
	newUser := func(name string) *imapmemserver.User {
		user := imapmemserver.NewUser(name, smokePass)
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
		return user
	}
	user := newUser(smokeUser)
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
		// A bulk sender names a list and a way to leave it and nothing
		// else; the list card has no row to show for it.
		{"Shop <news@shop.example>", "Our terms changed",
			"List-Id: campaign <c.list-id.shop.example>\r\n" +
				"List-Unsubscribe: <https://shop.example/u>, <mailto:leave@shop.example>\r\n" +
				"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n" +
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

	other := newUser(smokeUser2)
	draft := fmt.Sprintf("From: %s\r\nTo: friend@example.org\r\nSubject: unsent\r\n"+
		"Message-ID: <draft@test>\r\nContent-Type: text/plain\r\n\r\nLater.\r\n", smokeUser2)
	if _, err := other.Append("Drafts", literal{strings.NewReader(draft), int64(len(draft))},
		&imap.AppendOptions{Flags: []imap.Flag{imap.FlagDraft}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// An INBOX message for the second account, to star while a page is
	// scoped to it: the flag must reach the account the URL names, not
	// the first-listed one.
	inbox := fmt.Sprintf("From: pat@example.org\r\nTo: %s\r\nSubject: for the second account\r\n"+
		"Message-ID: <second-inbox@test>\r\nContent-Type: text/plain\r\n\r\nHello.\r\n", smokeUser2)
	if _, err := other.Append("INBOX", literal{strings.NewReader(inbox), int64(len(inbox))}, &imap.AppendOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mem.AddUser(other)
	return serveIMAP(t, mem, caps)
}

// smokeElsewhere is an account of another domain, whose server a test
// can take away while the first one keeps answering.
const smokeElsewhere = "c@elsewhere.local"

// startIMAPElsewhere is that domain's server: one account, one message.
func startIMAPElsewhere(t *testing.T) (string, *imapserver.Server) {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(smokeElsewhere, smokePass)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	raw := fmt.Sprintf("From: gil@example.org\r\nTo: %s\r\nSubject: from elsewhere\r\n"+
		"Message-ID: <elsewhere@test>\r\nContent-Type: text/plain\r\n\r\nHello.\r\n", smokeElsewhere)
	if _, err := user.Append("INBOX", literal{strings.NewReader(raw), int64(len(raw))}, &imap.AppendOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mem.AddUser(user)
	return serveIMAP(t, mem, rigCaps())
}

func serveIMAP(t *testing.T, mem *imapmemserver.Server, caps imap.CapSet) (string, *imapserver.Server) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(c *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         caps,
		InsecureAuth: true,
	})
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String(), srv
}

type literal struct {
	io.Reader
	n int64
}

func (l literal) Size() int64 { return l.n }

// startAlborz brings the app up against that IMAP server.
func startAlborz(t *testing.T, imapAddr string, smtpAddr ...string) string {
	t.Helper()
	return startAlborzWith(t, append([]string{"test.local=imap+insecure://" + imapAddr},
		smtpUpstreams(smtpAddr)...))
}

func startAlborzWith(t *testing.T, upstreams []string) string {
	t.Helper()
	key := fernet.MustDecodeKeys("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")[0]
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	_, err := alborz.New(e, &alborz.Options{
		Upstreams:  upstreams,
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
		"/mailbox/INBOX", "/mailbox/INBOX?view=starred", "/mailbox/INBOX?query=redesign",
		"/mailbox/INBOX%2FLists",
		"/mailbox/Drafts", "/mailbox/Junk", "/mailbox/Trash",
		"/compose", "/new-mailbox", "/settings", "/settings/account",
		"/settings/servers", "/signatures", "/signatures/create",
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
	// The listing cache is one per process and keyed by user, so a
	// fresh rig would still be read through what the last test left
	// in it. Asking for a refresh is what a reader does; here it is
	// what isolates the tests.
	postForm(t, c, base+"/mailbox/INBOX/refresh", nil)
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

// TestOpeningAHeldMessageAfterAFlagChange covers a crash that followed
// every star. A flag change evicts the mailbox's listing so the next
// list is fetched fresh, but the body fetched ahead for the message
// stays held, and the page served from a held body read its sidebar
// off the listing entry that was no longer there. The prefetch runs
// behind the listing, so the sequence is tried more than once to be
// sure of meeting a held body; and the session has to still serve
// mail afterwards, since a panic in a handler has cost the connection
// before.
func TestOpeningAHeldMessageAfterAFlagChange(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)

	for round := 0; round < 3; round++ {
		uids := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
		if len(uids) == 0 {
			t.Fatal("no messages listed")
		}
		time.Sleep(150 * time.Millisecond)
		next := "/message/INBOX/" + uids[0] + "?part=1"
		resp := postForm(t, c, base+"/message/INBOX/flag", url.Values{
			"uids": {uids[0]}, "flags": {`\Flagged`}, "action": {"add"}, "next": {next}})
		resp.Body.Close()
		if !strings.Contains(get(t, c, base+next), "message-flag-form") {
			t.Fatal("the message page did not render after the flag change")
		}
	}
	if body := get(t, c, base+"/mailbox/INBOX"); !strings.Contains(body, "message-row") {
		t.Fatal("the session stopped serving mail after the flag change")
	}
}

func TestCachedMessageAndSidebarWithoutUpstream(t *testing.T) {
	addr, upstream := startIMAPServer(t, rigCaps())
	base := startAlborz(t, addr)
	c := login(t, base)
	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	path := base + "/message/INBOX/" + uids[0] + "?part=1"
	read := func(path, target string) (string, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("HX-Request", "true")
		req.Header.Set("HX-Target", target)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("response %d: %v", resp.StatusCode, err)
		}
		return string(body), resp.Header.Get("Server-Timing")
	}
	warm := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		_, timing := read(path, "body")
		if strings.Contains(timing, "body_hit") {
			warm = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !warm {
		t.Fatal("message body never warmed")
	}
	fragment, _ := read(path, "message-navigation")
	if !strings.Contains(fragment, `id="message-navigation"`) || strings.Contains(fragment, "<!DOCTYPE") {
		t.Fatal("navigation endpoint did not return a fragment")
	}
	upstream.Close()
	c.Timeout = time.Second
	body, timing := read(path, "body")
	if !strings.Contains(body, "message-flag-form") || !strings.Contains(timing, "body_hit") || strings.Contains(timing, "imap_work") {
		t.Fatalf("cached message used upstream: %s", timing)
	}
	body, _ = read(base+"/mail/sidebar?path=/message/INBOX/"+uids[0], "aside")
	if !strings.Contains(body, "data-mail-sidebar") || strings.Contains(body, "message-flag-form") {
		t.Fatal("sidebar response rendered the message")
	}
}

// TestStarStickToTheScopedAccount stars a message on a page scoped to
// the second account and reads it back: the flag must land on the
// account the URL names, and the reloaded page must show it, not the
// colour it had before. This is the star that "returned the same
// colour" when the page belonged to a non-first account.
func TestStarStickToTheScopedAccount(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	postForm(t, c, base+"/login", url.Values{"username": {smokeUser2}, "password": {smokePass}})

	scoped := "?account=" + smokeUser2
	page := get(t, c, base+"/mailbox/INBOX"+scoped)
	uids := messageUIDs(page)
	if len(uids) == 0 {
		t.Fatal("the second account's inbox listed no message")
	}
	uid := uids[0]
	here := "/message/INBOX/" + uid + "?part=1&account=" + smokeUser2

	before := get(t, c, base+here)
	if flagButtonColor(t, before) != "" {
		t.Fatal("the message was already flagged before the test starred it")
	}
	postForm(t, c, base+"/message/INBOX/flag"+scoped,
		url.Values{"uids": {uid}, "color": {"red"}, "next": {here}})

	after := get(t, c, base+here)
	if got := flagButtonColor(t, after); got != "red" {
		t.Fatalf("after starring on the scoped page the star is %q, not gold", got)
	}
}

// flagButtonColor reads the colour the message page's star shows: the
// flag-<colour> class on the flag button, or "" when it is unflagged.
func flagButtonColor(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `class="flag-button`)
	if i < 0 {
		t.Fatal("the message page has no star button")
	}
	start := i + len(`class="`)
	end := strings.IndexByte(page[start:], '"')
	classes := strings.Fields(page[start : start+end])
	for _, cls := range classes {
		if strings.HasPrefix(cls, "flag-") && cls != "flag-button" {
			return strings.TrimPrefix(cls, "flag-")
		}
	}
	return ""
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
	if n := strings.Count(buf.String(), "\nFrom ") + strings.Count(
		buf.String()[:min(6, buf.Len())], "From "); n < 2 {
		t.Errorf("expected two messages, found %d separators", n)
	}
	if strings.Contains(buf.String(), "<!DOCTYPE") {
		t.Error("the export carries an HTML page inside it")
	}

	// A whole list asked about names the list again, never its messages:
	// a field for each is a page of megabytes on a large folder.
	resp, err = c.PostForm(base+"/message/INBOX/export", url.Values{"everything": {"1"}, "next": {"/mailbox/INBOX"}, "ask": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	buf.ReadFrom(resp.Body)
	resp.Body.Close()
	if page := buf.String(); !strings.Contains(page, `name="everything"`) || strings.Contains(page, `name="uids"`) {
		t.Errorf("the export page of a whole list carries its messages, or not the list")
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
		form := url.Values{}
		if on {
			form.Set("send_html", "1")
		}
		resp, err := c.PostForm(base+"/settings/account", form)
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

// TestSendingAsAnotherAccountKeepsItsDraft opens a draft on one
// account and sends it as the other. The draft is named by the URL, so
// it is the first account's; the From choice only says who sends. The
// mailbox and UID meant one account's message and were once applied to
// the other's connection, which deleted whatever it held at that UID.
func TestSendingAsAnotherAccountKeepsItsDraft(t *testing.T) {
	smtpAddr, sent := startSMTP(t)
	base := startAlborz(t, startIMAP(t), smtpAddr)
	c := login(t, base)
	resp, err := c.PostForm(base+"/login",
		url.Values{"username": {smokeUser2}, "password": {smokePass}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	first := "?account=" + smokeUser
	second := "?account=" + smokeUser2
	sendFrom(t, c, base, "/compose"+first, get(t, c, base+"/compose"+first),
		"save_as_draft", "1")
	drafts := messageUIDs(get(t, c, base+"/mailbox/Drafts"+first))
	if len(drafts) != 1 {
		t.Fatalf("the draft was not saved on the first account; found %d", len(drafts))
	}
	if n := len(messageUIDs(get(t, c, base+"/mailbox/Drafts"+second))); n != 1 {
		t.Fatalf("the second account should start with one draft, found %d", n)
	}

	path := "/message/Drafts/" + drafts[0] + "/edit?part=1&account=" + smokeUser
	sendFrom(t, c, base, path, get(t, c, base+path), "from_account", smokeUser2)

	if !strings.Contains(sent.Last(), "From: <"+smokeUser2+">") {
		t.Errorf("the message did not leave as the chosen account:\n%s", headOf(sent.Last()))
	}
	if n := len(messageUIDs(get(t, c, base+"/mailbox/Drafts"+first))); n != 0 {
		t.Errorf("the sent draft is still on the first account")
	}
	if n := len(messageUIDs(get(t, c, base+"/mailbox/Drafts"+second))); n != 1 {
		t.Errorf("the second account's own draft is gone")
	}
	if n := len(messageUIDs(get(t, c, base+"/mailbox/Sent"+second))); n != 1 {
		t.Errorf("the sent copy is not on the sending account; found %d", n)
	}
}

// TestBadMessageReferenceIsTheCallersFault: a UID that is not a number
// is a malformed link, which is a 400 and not a failure of ours.
func TestBadMessageReferenceIsTheCallersFault(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	for _, path := range []string{"/message/INBOX/notanumber", "/message/INBOX/notanumber/raw",
		"/message/INBOX/notanumber/eml"} {
		resp, err := c.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s: %s", path, resp.Status)
		}
	}
}

// TestSendWithoutMessageIDStillSends: the id is a hidden field of the
// page, and a form that lost it used to crash the handler instead of
// getting an id of its own.
func TestSendWithoutMessageIDStillSends(t *testing.T) {
	smtpAddr, sent := startSMTP(t)
	base := startAlborz(t, startIMAP(t), smtpAddr)
	c := login(t, base)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, f := range [][2]string{{"to", "friend@example.org"}, {"subject", "no id"}, {"text", "Body."}} {
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
	if resp.StatusCode >= 400 {
		t.Fatalf("POST /compose without message_id: %s", resp.Status)
	}
	if !strings.Contains(sent.Last(), "Message-Id: <") {
		t.Errorf("the message left without an id:\n%s", headOf(sent.Last()))
	}
}

// TestRefusedSendKeepsItsUploads: a refused send comes back on the
// form, and the next Send must still carry the file. The upload was
// once taken from the visit before SMTP was tried, and the retry went
// out without it and said nothing.
func TestRefusedSendKeepsItsUploads(t *testing.T) {
	smtpAddr, sent := startSMTP(t)
	base := startAlborz(t, startIMAP(t), smtpAddr)
	c := login(t, base)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("attachments", "kept.txt")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte("the attached words"))
	w.Close()
	resp, err := c.Post(base+"/compose/attachment", w.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := json.NewDecoder(resp.Body).Decode(&ids); err != nil || len(ids) != 1 {
		t.Fatalf("upload: %s, %v, %v", resp.Status, ids, err)
	}
	resp.Body.Close()

	resp = postCompose(t, c, base, "/compose", get(t, c, base+"/compose"),
		"to", "refused@example.org", "subject", "kept", "attachment-uuids", ids[0])
	var page bytes.Buffer
	page.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a refused recipient was answered with %s", resp.Status)
	}
	// What the script reads back into the form's uploads.
	kept := between(page.String(), `data-uuid="`, `"`)
	if kept != ids[0] {
		t.Fatalf("the refused form holds upload %q, want %q", kept, ids[0])
	}

	sendFrom(t, c, base, "/compose", page.String(), "subject", "kept", "attachment-uuids", kept)
	got := sent.Last()
	if !strings.Contains(got, "Subject: kept") {
		t.Fatalf("the retry was not sent:\n%s", headOf(got))
	}
	if !strings.Contains(got, `filename=kept.txt`) && !strings.Contains(got, `filename="kept.txt"`) {
		t.Errorf("the retry left without its attachment:\n%s", headOf(got))
	}
}

// TestEverySenderShapeOfAFileIsListed: senders mark a file in more
// ways than Content-Disposition: attachment, and a file marked another
// way was shown nowhere - every file Apple Mail sends among them - while
// an attached text file took the place of the message. The page and the
// zip must agree on the files, and the body must stay the body.
func TestEverySenderShapeOfAFileIsListed(t *testing.T) {
	addr := startIMAP(t)
	base := startAlborz(t, addr)
	c := login(t, base)

	pdf := "Content-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQgZmFrZQo=\r\n"
	text := "Content-Type: text/plain; charset=utf-8\r\n\r\nBody text.\r\n"
	shapes := []struct {
		name  string
		parts []string
		top   string
		files []string
		body  string
		// open is a part to open as well, and shows what it must show,
		// hides what it must not.
		open  string
		shows []string
		hides []string
	}{
		{"disposition attachment", []string{text,
			"Content-Type: application/pdf; name=\"a.pdf\"\r\nContent-Disposition: attachment; filename=\"a.pdf\"\r\n" + pdf},
			"mixed", []string{"a.pdf"}, "Body text.", "", nil, nil},
		{"inline with a filename, as Apple Mail sends", []string{text,
			"Content-Type: application/pdf; name=\"b.pdf\"\r\nContent-Disposition: inline; filename=b.pdf\r\n" + pdf},
			"mixed", []string{"b.pdf"}, "Body text.", "", nil, nil},
		{"a name on the type alone", []string{text, "Content-Type: application/pdf; name=\"c.pdf\"\r\n" + pdf},
			"mixed", []string{"c.pdf"}, "Body text.", "", nil, nil},
		{"no name and not text", []string{text, "Content-Type: application/octet-stream\r\n" + pdf},
			"mixed", []string{"Unnamed file"}, "Body text.", "", nil, nil},
		{"an attached text file", []string{text,
			"Content-Type: text/plain; charset=utf-8; name=\"notes.txt\"\r\nContent-Disposition: inline; filename=\"notes.txt\"\r\n\r\nThese are notes.\r\n"},
			"mixed", []string{"notes.txt"}, "Body text.", "", nil, nil},
		{"the file inside Apple Mail's HTML", []string{
			"Content-Type: text/plain; charset=utf-8\r\n\r\nBody text.\r\n",
			"Content-Type: multipart/mixed; boundary=\"inner\"\r\n\r\n--inner\r\n" +
				"Content-Type: text/html; charset=utf-8\r\n\r\n<p>Above.</p>\r\n--inner\r\n" +
				"Content-Type: application/pdf; name=\"apple.pdf\"\r\nContent-Disposition: inline; filename=apple.pdf\r\n" + pdf +
				"--inner\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Below.</p>\r\n--inner--\r\n"},
			"alternative", []string{"apple.pdf"}, "Body text.",
			// The pieces read as one, as they do in Apple Mail.
			"2.3", []string{"Above.", "Below."}, nil},
		{"an image the HTML shows by its Content-ID", []string{
			"Content-Type: text/html; charset=utf-8\r\n\r\n<p>Body text. <img src=\"cid:pic\"></p>\r\n",
			"Content-Type: image/png; name=\"pic.png\"\r\nContent-Disposition: inline; filename=\"pic.png\"\r\nContent-ID: <pic>\r\n" + pdf},
			"related", nil, "Body text.", "", nil, nil},
		{"the signature of a signed message", []string{text,
			"Content-Type: application/pgp-signature; name=\"signature.asc\"\r\nContent-Disposition: attachment; filename=\"signature.asc\"\r\n\r\n-----BEGIN PGP SIGNATURE-----\r\n"},
			"signed; protocol=\"application/pgp-signature\"", nil, "Body text.", "", nil, nil},
		{"a forwarded message", []string{text,
			"Content-Type: message/rfc822\r\n\r\nFrom: <x@example.org>\r\nSubject: Inner\r\n\r\nInner body.\r\n"},
			"mixed", []string{"Inner.eml"}, "Body text.", "", nil, nil},
		{"text beside HTML that shows an image", []string{text,
			"Content-Type: multipart/related; boundary=\"rel\"\r\n\r\n--rel\r\n" +
				"Content-Type: text/html; charset=utf-8\r\n\r\n<p>Body text. <img src=\"cid:logo\"></p>\r\n--rel\r\n" +
				"Content-Type: image/png; name=\"logo.png\"\r\nContent-ID: <logo>\r\n" + pdf + "--rel--\r\n"},
			// The image belongs to the HTML, which is the other version
			// of this message; the text the reader chose is words alone.
			"alternative", nil, "Body text.", "1", nil, []string{`class="shown-image"`}},
		{"text and the image it shows itself", []string{text,
			"Content-Type: image/png; name=\"logo.png\"\r\nContent-ID: <logo>\r\n" + pdf},
			// Beside the text and named by no file list, so the page
			// shows it rather than losing it.
			"related", nil, "Body text.", "1", []string{`class="shown-image" src="/message/Shapes/11/raw?part=2"`}, nil},
	}

	ic, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ic.Close()
	if err := ic.Login(smokeUser, smokePass).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := ic.Create("Shapes", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	for _, s := range shapes {
		var b strings.Builder
		fmt.Fprintf(&b, "From: <eve@example.org>\r\nTo: <%s>\r\nSubject: %s\r\nMIME-Version: 1.0\r\n"+
			"Content-Type: multipart/%s; boundary=\"outer\"\r\n\r\n", smokeUser, s.name, s.top)
		for _, p := range s.parts {
			b.WriteString("--outer\r\n" + p)
		}
		b.WriteString("--outer--\r\n")
		cmd := ic.Append("Shapes", int64(b.Len()), nil)
		cmd.Write([]byte(b.String()))
		cmd.Close()
		if _, err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}

	listed := regexp.MustCompile(`>\s*([^<>]+?)\s*</a> \([a-z]+/[^,]+, `)
	for i, s := range shapes {
		path := fmt.Sprintf("/message/Shapes/%d", i+1)
		page := get(t, c, base+path)
		if !strings.Contains(page, html.EscapeString(s.name)) {
			t.Fatalf("%s: %s is not the message appended", s.name, path)
		}
		var files []string
		for _, m := range listed.FindAllStringSubmatch(page, -1) {
			files = append(files, m[1])
		}
		if !slices.Equal(files, s.files) {
			t.Errorf("%s: the page lists %q, want %q", s.name, files, s.files)
		}
		if !strings.Contains(page, s.body) || strings.Contains(page, "These are notes.") {
			t.Errorf("%s: the body is not the message's text", s.name)
		}
		resp, err := c.Get(base + path + "/attachments.zip")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatalf("%s: attachments.zip: %v", s.name, err)
		}
		if len(archive.File) != len(s.files) {
			t.Errorf("%s: the zip holds %d files and the page lists %d", s.name, len(archive.File), len(s.files))
		}
		if s.open == "" {
			continue
		}
		opened := get(t, c, base+path+"?part="+s.open)
		for _, want := range s.shows {
			if !strings.Contains(html.UnescapeString(opened), want) {
				t.Errorf("%s: part %s does not show %q", s.name, s.open, want)
			}
		}
		for _, unwanted := range s.hides {
			if strings.Contains(html.UnescapeString(opened), unwanted) {
				t.Errorf("%s: part %s shows %q", s.name, s.open, unwanted)
			}
		}
	}
}

// TestUpstreamOutageIsNotOurFailure: a mail server that cannot be
// reached is answered with the page that says so, not with a 500 that
// sends the reader to check their password. The page depends on the
// error keeping its type through every wrap on the way up.
func TestUpstreamOutageIsNotOurFailure(t *testing.T) {
	addr, srv := startIMAPServer(t, rigCaps())
	base := startAlborz(t, addr)
	c := login(t, base)
	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}

	// The session's connection dies with the server; the next command
	// finds it logged out, dials again, and is refused. The reader's
	// goroutine notices the loss a moment after Close returns.
	srv.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := c.PostForm(base+"/message/INBOX/flag",
			url.Values{"uids": {uids[0]}, "flags": {"\\Seen"}})
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusBadGateway {
			if !strings.Contains(buf.String(), "did not answer") &&
				!strings.Contains(buf.String(), "could not reach") {
				t.Errorf("502 without the upstream page:\n%s", firstLines(buf.String()))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("an unreachable server was answered with %s:\n%s", resp.Status, firstLines(buf.String()))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestNewestFirstWithoutSort: a server without the SORT extension
// answers SEARCH oldest first, and the list once showed it that way,
// with the oldest message on top and the neighbours swapped.
func TestNewestFirstWithoutSort(t *testing.T) {
	caps := rigCaps()
	delete(caps, imap.CapSort)
	addr, _ := startIMAPServer(t, caps)
	base := startAlborz(t, addr)
	c := login(t, base)

	// A search is what goes through SEARCH; the plain list is fetched
	// by sequence range and was never wrong.
	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX?query=example"))
	if len(uids) < 2 {
		t.Fatalf("need two seeded messages to match, found %d", len(uids))
	}
	for i := 1; i < len(uids); i++ {
		if len(uids[i-1]) < len(uids[i]) || uids[i-1] < uids[i] {
			t.Fatalf("older message listed first: %v", uids)
		}
	}
}

// TestComposeOpensFromTheAccountNamed: with two accounts signed in, a
// compose page opened for one of them must offer that account in From.
// The dropdown once selected nothing unless the message named an
// identity verbatim, and a browser then showed the first account.
func TestComposeOpensFromTheAccountNamed(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	resp, err := c.PostForm(base+"/login",
		url.Values{"username": {smokeUser2}, "password": {smokePass}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	for _, account := range []string{smokeUser, smokeUser2} {
		page := get(t, c, base+"/compose?account="+account)
		if !strings.Contains(page, `value="`+account+`" selected`) {
			t.Errorf("compose for %s does not select it in From", account)
		}
	}
	// No account is "active": a bare compose opens on the first listed.
	if page := get(t, c, base+"/compose"); !strings.Contains(page, `value="`+smokeUser+`" selected`) {
		t.Errorf("compose without an account does not select the first listed one")
	}
}

// postForm submits a form and hands back the answer without following
// its redirect, which is where a handler says what it did.
func postForm(t *testing.T, c *http.Client, u string, form url.Values) *http.Response {
	t.Helper()
	stay := *c
	stay.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := stay.PostForm(u, form)
	if err != nil {
		t.Fatalf("POST %s: %v", u, err)
	}
	resp.Body.Close()
	return resp
}

// TestMessagesMoveDeleteAndFlag drives the three actions the list
// toolbar offers and reads the result back from the folders.
func TestMessagesMoveDeleteAndFlag(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
	if len(uids) < 3 {
		t.Fatalf("need three seeded messages, found %d", len(uids))
	}

	if resp := postForm(t, c, base+"/message/INBOX/move", url.Values{"uids": {uids[0]}, "to": {"Archive"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("move: %s", resp.Status)
	}
	if n := len(messageUIDs(get(t, c, base+"/mailbox/Archive"))); n != 1 {
		t.Errorf("Archive holds %d messages after a move, want 1", n)
	}

	if resp := postForm(t, c, base+"/message/INBOX/delete", url.Values{"uids": {uids[1]}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("delete: %s", resp.Status)
	}
	left := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
	if len(left) != len(uids)-2 || slices.Contains(left, uids[1]) {
		t.Errorf("INBOX after a move and a delete: %v", left)
	}

	if resp := postForm(t, c, base+"/message/INBOX/flag", url.Values{"uids": {uids[2]}, "flags": {"\\Flagged"}, "action": {"add"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("flag: %s", resp.Status)
	}
	if starred := messageUIDs(get(t, c, base+"/mailbox/INBOX?view=starred")); len(starred) != 1 || starred[0] != uids[2] {
		t.Errorf("starred view after flagging %s: %v", uids[2], starred)
	}
}

func TestStarViewKeepsItsQuery(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	refs := regexp.MustCompile(`name="refs" value="([^"]+)"`)
	var starred []string
	for _, query := range []string{"from:eve", "from:hal"} {
		uids := messageUIDs(get(t, c, base+"/mailbox/INBOX?query="+url.QueryEscape(query)))
		if len(uids) != 1 {
			t.Fatalf("%q answers %d messages in INBOX, want 1", query, len(uids))
		}
		starred = append(starred, uids[0])
	}
	postForm(t, c, base+"/message/INBOX/flag", url.Values{"uids": starred, "flags": {"\\Flagged"}, "action": {"add"}})

	narrowed := "/search?view=starred&query=" + url.QueryEscape("from:eve")
	if rows := refs.FindAllString(get(t, c, base+narrowed), -1); len(rows) != 1 {
		t.Fatalf("starred and from:eve lists %d rows, want the one that is both", len(rows))
	}
	resp := postForm(t, c, base+"/mailbox/INBOX/all/act?action=move&to=Archive", url.Values{"everything": {"1"}, "next": {narrowed}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("move: %s", resp.Status)
	}
	if n := len(messageUIDs(get(t, c, base+"/mailbox/Archive"))); n != 1 {
		t.Errorf("Archive holds %d after moving everything starred from eve, want 1", n)
	}
}

// TestFoldersAreMadeAndRemoved creates a top-level folder and deletes
// it again, checking the sidebar between the two.
func TestFoldersAreMadeAndRemoved(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)

	if resp := postForm(t, c, base+"/new-mailbox", url.Values{"name": {"Projects"}, "location": {"0"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("new folder: %s", resp.Status)
	}
	if page := get(t, c, base+"/mailbox/Projects"); !strings.Contains(page, "Projects") {
		t.Fatalf("the new folder is not listed")
	}
	if resp := postForm(t, c, base+"/delete-mailbox/Projects", nil); resp.StatusCode != http.StatusFound {
		t.Fatalf("delete folder: %s", resp.Status)
	}
	resp, err := c.Get(base + "/mailbox/Projects")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a deleted folder still answers %s", resp.Status)
	}
}

// TestAccountsScopeAndSignOut: the URL is the scope (ADR 0001). A bare
// mail URL over two accounts is the merged view, with both in the rail;
// one naming an account is that account's, and says so in the nav.
// Signing out of the last account ends the session.
func TestAccountsScopeAndSignOut(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	postForm(t, c, base+"/login", url.Values{"username": {smokeUser2}, "password": {smokePass}})

	merged := get(t, c, base+"/mailbox/INBOX")
	for _, account := range []string{smokeUser, smokeUser2} {
		if !strings.Contains(merged, `href="/mailbox/INBOX?account=`+account+`"`) {
			t.Errorf("the merged view's rail does not offer %s", account)
		}
	}
	if !strings.Contains(merged, `class="acct-folders aggregate-folders"`) {
		t.Errorf("the merged view has no merged entry in the rail")
	}
	scoped := get(t, c, base+"/mailbox/INBOX?account="+smokeUser2)
	if !strings.Contains(scoped, `popovertarget="menu-accounts"><span class="label"><bdi class="ltr">`+smokeUser2) {
		t.Errorf("a page scoped to %s does not say so in the nav", smokeUser2)
	}
	if !strings.Contains(scoped, `class="acct-folders aggregate-folders"`) {
		t.Errorf("the scoped view lost the merged entry from the rail")
	}

	for _, account := range []string{smokeUser, smokeUser2} {
		if resp := postForm(t, c, base+"/logout", url.Values{"account": {account}}); resp.StatusCode != http.StatusFound {
			t.Fatalf("logout %s: %s", account, resp.Status)
		}
	}
	resp := postForm(t, c, base+"/mailbox/INBOX", nil)
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Errorf("after signing out of every account a page still answered %s to %s", resp.Status, resp.Header.Get("Location"))
	}
}

func TestZipHoldsTheAttachmentsTheCardCounts(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX?query="+url.QueryEscape("from:bob")))
	if len(uids) != 1 {
		t.Fatalf("from:bob answers %d messages, want the one with attachments", len(uids))
	}
	listed := strings.Count(get(t, c, base+"/message/INBOX/"+uids[0]), "/raw?part=")
	if listed == 0 {
		t.Fatal("the message page lists no attachment")
	}
	resp, err := c.Get(base + "/message/INBOX/" + uids[0] + "/attachments.zip")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("attachments.zip: %v", err)
	}
	if len(archive.File) != listed {
		t.Errorf("the zip holds %d files and the page lists %d", len(archive.File), listed)
	}
}

// TestListCardOnlyWhenItHasRows: a bulk sender's List-ID opens no card,
// a list with a posting address does.
func TestListCardOnlyWhenItHasRows(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
	seen := map[string]bool{}
	for _, uid := range uids {
		page := get(t, c, base+"/message/INBOX/"+uid+"?part=1")
		card := strings.Contains(page, `class="list-card"`)
		switch {
		case strings.Contains(page, "[discuss] Threading"):
			seen["list"] = true
			if !card {
				t.Errorf("a list with a posting address has no card")
			}
		case strings.Contains(page, "Our terms changed"):
			seen["bulk"] = true
			if card {
				t.Errorf("a bulk sender with nothing but an unsubscribe got an empty card")
			}
		}
	}
	if !seen["list"] || !seen["bulk"] {
		t.Fatalf("the seeded list and bulk messages were not both found: %v", seen)
	}
}

// TestNextStaysOnTheSite covers the return address a form carries. It
// is the page the action was taken from, and the handler comes back to
// it; a value naming another host, or a path a browser reads as one,
// must fall back to the handler's own landing page.
func TestNextStaysOnTheSite(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	stay := *c
	stay.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	flag := func(next string) string {
		resp, err := stay.PostForm(base+"/message/INBOX/flag",
			url.Values{"uids": {uids[0]}, "flags": {"\\Seen"}, "next": {next}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("flag with next=%q: %s", next, resp.Status)
		}
		return resp.Header.Get("Location")
	}
	if to := flag("/mailbox/INBOX?all=1"); to != "/mailbox/INBOX?all=1" {
		t.Errorf("a page of ours was not returned to: %q", to)
	}
	for _, next := range []string{"https://evil.example/", "//evil.example/", "/\\evil.example/"} {
		if to := flag(next); to != "/message/INBOX/"+uids[0] {
			t.Errorf("next=%q redirected to %q", next, to)
		}
	}
}

// sendFrom submits the compose form exactly as the page presented it,
// plus any fields given as name, value pairs.
func sendFrom(t *testing.T, c *http.Client, base, path, page string, fields ...string) {
	t.Helper()
	resp := postCompose(t, c, base, path, page, fields...)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("POST %s: %s", path, resp.Status)
	}
}

// postCompose submits the page's form as the browser would, the later
// fields overriding the message it writes by default.
func postCompose(t *testing.T, c *http.Client, base, path, page string, fields ...string) *http.Response {
	t.Helper()
	mid := strings.NewReplacer("&lt;", "<", "&gt;", ">").Replace(
		between(page, `name="message_id" value="`, `"`))
	if mid == "" {
		t.Fatalf("%s carries no message id", path)
	}
	form := map[string]string{
		"message_id":  mid,
		"in_reply_to": between(page, `name="in_reply_to" value="`, `"`),
		"from":        smokeUser, "to": "friend@example.org",
		"subject": "direction", "text": "سلام\n\nEnglish.",
	}
	for i := 0; i+1 < len(fields); i += 2 {
		form[fields[i]] = fields[i+1]
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for name, value := range form {
		if err := w.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	resp, err := c.Post(base+path, w.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// A submission server that turns a recipient away has answered, and
// the answer belongs on the form with the message still in it. It once
// became an error page, the typed text gone with it.
func TestRefusedRecipientComesBackOnTheForm(t *testing.T) {
	smtpAddr, sent := startSMTP(t)
	base := startAlborz(t, startIMAP(t), smtpAddr)
	c := login(t, base)

	resp := postCompose(t, c, base, "/compose", get(t, c, base+"/compose"),
		"to", "refused@example.org", "subject", "kept")
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a refused recipient answered %s, want 422", resp.Status)
	}
	form := string(page)
	if !strings.Contains(form, `role="alert"`) || !strings.Contains(form, "User unknown") {
		t.Errorf("the server's refusal is not on the form:\n%s", form)
	}
	if !strings.Contains(form, `value="kept"`) {
		t.Errorf("the typed subject did not survive the refusal")
	}
	if sent.Last() != "" {
		t.Errorf("a refused message reached the wire:\n%s", headOf(sent.Last()))
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

// noticeAction reads what the header bar offers after a redirect: the
// path and hidden fields of a form that posts, or the href of a link,
// and nothing when the bar carries no action.
func noticeAction(body string) (string, url.Values) {
	m := regexp.MustCompile(`(?s)<div class="notice[^"]*"[^>]*>(.*?)</div>`).FindStringSubmatch(body)
	if m == nil {
		return "", nil
	}
	bar := m[1]
	if f := regexp.MustCompile(`<form method="post" action="([^"]+)" class="notice-action">`).FindStringSubmatch(bar); f != nil {
		fields := url.Values{}
		for _, in := range regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(bar, -1) {
			fields.Add(html.UnescapeString(in[1]), html.UnescapeString(in[2]))
		}
		return html.UnescapeString(f[1]), fields
	}
	if a := regexp.MustCompile(`<a class="notice-action" href="([^"]+)">`).FindStringSubmatch(bar); a != nil {
		return html.UnescapeString(a[1]), nil
	}
	return "", nil
}

// TestMoveNoticeUndoes follows the undo a move's notice offers, reads
// the folders back, and checks that the undo's own notice offers
// nothing: an undo with an undo would loop.
func TestMoveNoticeUndoes(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
	if len(uids) == 0 {
		t.Fatal("the rig seeded no messages")
	}
	postForm(t, c, base+"/message/INBOX/move", url.Values{"uids": {uids[0]}, "to": {"Archive"}})
	action, fields := noticeAction(get(t, c, base+"/mailbox/INBOX"))
	if fields.Get("undo") == "" {
		t.Fatalf("a move's notice offers no undo: %q %v", action, fields)
	}
	if resp := postForm(t, c, base+action, fields); resp.StatusCode != http.StatusFound {
		t.Fatalf("undo: %s", resp.Status)
	}
	body := get(t, c, base+"/mailbox/INBOX")
	if n := len(messageUIDs(body)); n != len(uids) {
		t.Errorf("INBOX holds %d after the undo, want %d", n, len(uids))
	}
	if n := len(messageUIDs(get(t, c, base+"/mailbox/Archive"))); n != 0 {
		t.Errorf("Archive still holds %d after the undo", n)
	}
	if action, _ := noticeAction(body); action != "" {
		t.Errorf("the undo's notice offers %q", action)
	}
}

func TestSearchMoveUndoesIntoTheFolderItCameFrom(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	lists := base + "/mailbox/" + url.PathEscape("INBOX/Lists")
	inbox, nested := messageUIDs(get(t, c, base+"/mailbox/INBOX")), messageUIDs(get(t, c, lists))
	if len(nested) == 0 {
		t.Fatal("the rig seeded no messages in INBOX/Lists")
	}
	results := "/search?query=" + url.QueryEscape("from:eve in:lists")
	postForm(t, c, base+"/mailbox/INBOX/all/act?action=move&to=Archive",
		url.Values{"refs": {smokeUser + "|INBOX/Lists|" + nested[0]}, "next": {results}})
	moved := get(t, c, lists)
	if n := len(messageUIDs(moved)); n != len(nested)-1 {
		t.Fatalf("INBOX/Lists holds %d after the move, want %d", n, len(nested)-1)
	}
	action, fields := noticeAction(moved)
	if fields.Get("undo") == "" {
		t.Fatalf("the move's notice offers no undo: %q %v", action, fields)
	}
	postForm(t, c, base+action, fields)
	if n := len(messageUIDs(get(t, c, lists))); n != len(nested) {
		t.Errorf("INBOX/Lists holds %d after the undo, want %d", n, len(nested))
	}
	if n := len(messageUIDs(get(t, c, base+"/mailbox/INBOX"))); n != len(inbox) {
		t.Errorf("INBOX holds %d after the undo, want %d", n, len(inbox))
	}
}

func TestDroppedTwiceIsImportedOnce(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	eml := "From: ann@example.org\r\nTo: " + smokeUser + "\r\nSubject: dropped\r\n" +
		"Message-ID: <dropped@test>\r\nContent-Type: text/plain\r\n\r\nBody.\r\n"
	for range 2 {
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		part, err := w.CreateFormFile("file", "dropped.eml")
		if err != nil {
			t.Fatal(err)
		}
		part.Write([]byte(eml))
		w.Close()
		resp, err := c.Post(base+"/mailbox/Archive/import", w.FormDataContentType(), &body)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("drop: %s", resp.Status)
		}
	}
	if n := len(messageUIDs(get(t, c, base+"/mailbox/Archive"))); n != 1 {
		t.Errorf("Archive holds %d after the same file dropped twice, want 1", n)
	}
}

// TestDeleteNoticeCountsTheRest deletes a whole page and expects the
// notice to offer the rest of the folder by its count, through a page
// that says the count again; a partial page must offer nothing.
func TestDeleteNoticeCountsTheRest(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
	if len(uids) < 3 {
		t.Fatalf("need three seeded messages, found %d", len(uids))
	}
	postForm(t, c, base+"/message/INBOX/delete?ipp=2", url.Values{"uids": uids[:2]})
	body := get(t, c, base+"/mailbox/INBOX")
	action, fields := noticeAction(body)
	if fields != nil || action != "/mailbox/INBOX/empty" {
		t.Fatalf("a full page's notice offers %q %v", action, fields)
	}
	rest := fmt.Sprintf("%d", len(uids)-2)
	if !strings.Contains(body, "Delete all "+rest+" in this folder") {
		t.Errorf("the notice does not count the rest: %s", noticeBar(body))
	}
	if page := get(t, c, base+action); !strings.Contains(page, "It holds "+rest+" messages") {
		t.Errorf("the empty page does not say what it holds: %s", alertText(page))
	}
	postForm(t, c, base+"/message/INBOX/delete?ipp=2", url.Values{"uids": uids[2:3]})
	if action, _ := noticeAction(get(t, c, base+"/mailbox/INBOX")); action != "" {
		t.Errorf("a partial page's notice offers %q", action)
	}
}

func noticeBar(body string) string {
	return regexp.MustCompile(`(?s)<div class="notice[^"]*"[^>]*>(.*?)</div>`).FindString(body)
}

func alertText(body string) string {
	return regexp.MustCompile(`(?s)<div class="alert"[^>]*>(.*?)</div>`).FindString(body)
}

// bothAccounts signs in twice, so the bag holds two, and returns the
// client that carries them.
func bothAccounts(t *testing.T, base string) *http.Client {
	t.Helper()
	c := login(t, base)
	addAccount(t, c, base, smokeUser2)
	return c
}

func addAccount(t *testing.T, c *http.Client, base, account string) {
	t.Helper()
	resp, err := c.PostForm(base+"/login?add=1",
		url.Values{"username": {account}, "password": {smokePass}})
	if err != nil {
		t.Fatalf("second sign-in: %v", err)
	}
	resp.Body.Close()
}

// rowRefs names every row a merged listing shows, in the order shown.
func rowRefs(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`name="refs" value="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

// TestMergedListsCutTheSamePage: the merged folder and the search
// across folders both cut a page out of a merge. The second page is
// where a cut that is off by one shows, or a total that is one
// account's.
func TestMergedListsCutTheSamePage(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := bothAccounts(t, base)
	for _, list := range []string{"/mailbox/INBOX?", "/search?query=example&"} {
		all := rowRefs(get(t, c, base+list+"ipp=100"))
		if len(all) < 5 {
			t.Fatalf("%s: need five seeded rows to have a third page, found %d", list, len(all))
		}
		body := get(t, c, base+list+"ipp=2&page=1")
		if got := rowRefs(body); !slices.Equal(got, all[2:4]) {
			t.Errorf("%s: the second page holds %v, want %v", list, got, all[2:4])
		}
		if want := fmt.Sprintf("3–4 of %d", len(all)); !strings.Contains(body, want) {
			t.Errorf("%s: the second page does not say %q", list, want)
		}
		for _, page := range []string{"?page=0&", "?page=2&"} {
			if !strings.Contains(html.UnescapeString(body), page) {
				t.Errorf("%s: the second page has no link to %s", list, page)
			}
		}
	}
}

// TestAServerDownCostsItsRowsNotThePage: with one account's server
// gone, the merged folder and the search still show the other
// account's rows, and say whose are missing.
func TestAServerDownCostsItsRowsNotThePage(t *testing.T) {
	elsewhere, srv := startIMAPElsewhere(t)
	base := startAlborzWith(t, []string{
		"test.local=imap+insecure://" + startIMAP(t),
		"elsewhere.local=imap+insecure://" + elsewhere})
	c := login(t, base)
	addAccount(t, c, base, smokeElsewhere)
	if rows := rowAccounts(get(t, c, base+"/mailbox/INBOX")); rows[smokeUser] == 0 || rows[smokeElsewhere] == 0 {
		t.Fatalf("the merged inbox is not both accounts: %v", rows)
	}

	srv.Close()
	for _, list := range []string{"/mailbox/INBOX", "/search?query=example"} {
		// The connection's loss is noticed a moment after Close returns,
		// and until then the cache answers for the account.
		var body string
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			postForm(t, c, base+"/mailbox/INBOX/refresh", nil)
			body = get(t, c, base+list)
			if strings.Contains(body, "did not answer") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: nothing says a server did not answer: %s", list, noticeBar(body))
			}
		}
		if !strings.Contains(noticeBar(body), smokeElsewhere) {
			t.Errorf("%s: the notice does not name the account: %s", list, noticeBar(body))
		}
		if rows := rowAccounts(body); rows[smokeUser] == 0 || rows[smokeElsewhere] != 0 {
			t.Errorf("%s: rows by account are %v", list, rows)
		}
	}
}

// rowAccounts names the account of every row a listing shows, from the
// reference its checkbox carries.
func rowAccounts(body string) map[string]int {
	out := map[string]int{}
	for _, m := range regexp.MustCompile(`name="refs" value="([^|]+)\|`).FindAllStringSubmatch(body, -1) {
		out[m[1]]++
	}
	return out
}

// TestScopeSelectsAccounts pins what the URL means: no account named
// is every account, and one named is that one alone. The scope is the
// only thing that decides, so a page that quietly falls back to a
// current account shows up here.
func TestScopeSelectsAccounts(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := bothAccounts(t, base)

	merged := rowAccounts(get(t, c, base+"/mailbox/INBOX"))
	if merged[smokeUser] == 0 || merged[smokeUser2] == 0 {
		t.Fatalf("the merged inbox is not both accounts: %v", merged)
	}
	for _, account := range []string{smokeUser, smokeUser2} {
		scoped := get(t, c, base+"/mailbox/INBOX?account="+account)
		if n := len(messageUIDs(scoped)); n == 0 {
			t.Errorf("%s has no rows when the page is scoped to it", account)
		}
		if strings.Contains(scoped, `value="`+other(account)+`|`) {
			t.Errorf("a page scoped to %s shows %s's mail", account, other(account))
		}
	}
}

func other(account string) string {
	if account == smokeUser {
		return smokeUser2
	}
	return smokeUser
}

// TestNoticeReachesTheReaderFromAnyScope acts on the second account and
// comes back to the merged view. A notice belongs to the person, not to
// the account the action named, and one filed under an account waits
// for a page scoped to it and turns up later on something unrelated.
func TestNoticeReachesTheReaderFromAnyScope(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := bothAccounts(t, base)

	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX?account="+smokeUser2))
	if len(uids) == 0 {
		t.Fatal("the second account has no message to act on")
	}
	resp := postForm(t, c, base+"/message/INBOX/move?account="+smokeUser2,
		url.Values{"uids": {uids[0]}, "to": {"Archive"}, "next": {"/mailbox/INBOX"}})
	resp.Body.Close()
	if !strings.Contains(get(t, c, base+"/mailbox/INBOX"), "notice-text") {
		t.Error("the merged view did not carry the notice the action raised")
	}
}

// TestAVisitOpensOnlyToItsOwnSecret: the visit's id is printed on the
// page of browsers signed in, for every other browser of the account to
// read, so the id alone must open nothing.
func TestAVisitOpensOnlyToItsOwnSecret(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)

	at, _ := url.Parse(base)
	var id string
	for _, ck := range c.Jar.Cookies(at) {
		if ck.Name == "alborz_visit" {
			id, _, _ = strings.Cut(ck.Value, ".")
		}
	}
	if id == "" {
		t.Fatal("signing in left no visit cookie")
	}
	if uids := messageUIDs(get(t, c, base+"/mailbox/INBOX")); len(uids) == 0 {
		t.Fatal("the visit's own browser was not shown its inbox")
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/mailbox/INBOX", nil)
	req.AddCookie(&http.Cookie{Name: "alborz_visit", Value: id + ".x"})
	other := &http.Client{Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := other.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("the inbox opened to a cookie holding the id and another secret")
	}
}
