package alborz_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/auth"
	"git.mehdix.org/alborz/filters"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/contacts"
	"git.mehdix.org/alborz/settings"
	"git.mehdix.org/alborz/web"
	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-mbox"
	"github.com/emersion/go-message"
	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
)

const smokeUser = "a@test.local"
const smokeUser2 = "b@test.local"
const smokePass = "x"

// sentMail collects what the server actually put on the wire.
type sentMail struct {
	mu       sync.Mutex
	last     string
	all      []string
	sessions int
}

func (s *sentMail) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions
}

func (s *sentMail) Last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *sentMail) All() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.all)
}

type smtpBackend struct{ got *sentMail }

func (b *smtpBackend) NewSession(_ *smtp.Conn) (smtp.Session, error) {
	b.got.mu.Lock()
	b.got.sessions++
	b.got.mu.Unlock()
	return &smtpSession{got: b.got}, nil
}

type smtpSession struct{ got *sentMail }

// Any credentials pass: this stands in for a server, it does not test one.
func (s *smtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *smtpSession) Auth(mech string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(identity, username, password string) error {
		return nil
	}), nil
}

func (s *smtpSession) Mail(string, *smtp.MailOptions) error { return nil }

// A recipient at refused@ is turned away; every other one is taken.
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
	s.got.all = append(s.got.all, string(b))
	s.got.mu.Unlock()
	return nil
}

// startSMTP leaves the extensions named in hidden out of its EHLO.
func startSMTP(t *testing.T, hidden ...string) (string, *sentMail) {
	t.Helper()
	got := &sentMail{}
	srv := smtp.NewServer(&smtpBackend{got: got})
	srv.AllowInsecureAuth = true
	srv.Domain = "localhost"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ehloHiding{ln, hidden})
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String(), got
}

type ehloHiding struct {
	net.Listener
	hidden []string
}

func (l ehloHiding) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	return ehloHidingConn{c, l.hidden}, err
}

type ehloHidingConn struct {
	net.Conn
	hidden []string
}

func (c ehloHidingConn) Write(b []byte) (int, error) {
	out := b
	for _, ext := range c.hidden {
		out = bytes.ReplaceAll(out, []byte("250-"+ext+"\r\n"), nil)
	}
	if _, err := c.Conn.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}

// The second account's draft makes one deleted on the wrong account go missing.
func startIMAP(t *testing.T) string {
	t.Helper()
	addr, _ := startIMAPServer(t, rigCaps())
	return addr
}

// METADATA is not listed: the in-memory server does not implement it.
func rigCaps() imap.CapSet {
	return imap.CapSet{
		imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {},
		imap.CapCondStore: {}, imap.CapSort: {},
	}
}

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
			// A nested folder's name carries the separator, which every URL must escape.
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
	// Each renders different toolbars and rows, so each branch gets opened.
	for _, m := range []struct{ from, subj, extra, body string }{
		{"Eve <eve@example.org>", "Ideas for the redesign",
			"Content-Type: text/plain; charset=UTF-8\r\n", "Body.\r\n"},
		{"Frank <frank@example.org>", "[discuss] Threading",
			"List-Id: Rig discuss <discuss.lists.example.org>\r\n" +
				"List-Post: <mailto:discuss@lists.example.org>\r\n" +
				"List-Unsubscribe: <https://lists.example.org/u>\r\n" +
				"Content-Type: text/plain; charset=UTF-8\r\n", "Body.\r\n"},
		{"Shop <news@shop.example>", "Our terms changed",
			"List-Id: campaign <c.list-id.shop.example>\r\n" +
				"List-Unsubscribe: <https://shop.example/u>, <mailto:leave@shop.example>\r\n" +
				"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n" +
				"Content-Type: text/plain; charset=UTF-8\r\n", "Body.\r\n"},
		// The sanitizer builds the part's raw URL, which must escape the folder's separator.
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
				// An unnamed attachment exercises the branch named ones skip.
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
	// The flag must reach the account the URL names, not the first-listed one.
	inbox := fmt.Sprintf("From: pat@example.org\r\nTo: %s\r\nSubject: for the second account\r\n"+
		"Message-ID: <second-inbox@test>\r\nContent-Type: text/plain\r\n\r\nHello.\r\n", smokeUser2)
	if _, err := other.Append("INBOX", literal{strings.NewReader(inbox), int64(len(inbox))}, &imap.AppendOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mem.AddUser(other)
	return serveIMAP(t, mem, caps)
}

const smokeElsewhere = "c@elsewhere.local"

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

func startAlborz(t *testing.T, imapAddr string, smtpAddr ...string) string {
	t.Helper()
	return startAlborzWith(t, append([]string{"test.local=imap+insecure://" + imapAddr},
		smtpUpstreams(smtpAddr)...))
}

func startAlborzWith(t *testing.T, upstreams []string) string {
	t.Helper()
	return serveAlborz(t, upstreams, true)
}

var smokeKey = fernet.MustDecodeKeys("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")[0]

// newAlborz wires the features as cmd/alborz does.
func newAlborz(t *testing.T, e *echo.Echo, options *alborz.Options) {
	t.Helper()
	s, err := web.New(e, options, mail.TemplateFuncs)
	if err != nil {
		t.Fatalf("start alborz: %v", err)
	}
	auth.Mount(s)
	mail.Mount(s)
	settings.Mount(s)
	st, err := pim.Mount(s)
	if err != nil {
		t.Fatalf("start alborz: %v", err)
	}
	if err := calendar.Mount(s, st); err != nil {
		t.Fatalf("start alborz: %v", err)
	}
	if err := contacts.Mount(s, st); err != nil {
		t.Fatalf("start alborz: %v", err)
	}
	filters.Mount(s)
}

func serveAlborz(t *testing.T, upstreams []string, cache bool) string {
	t.Helper()
	return serveAlborzAt(t, func(string) []string { return upstreams }, cache, nil)
}

// serveAlborzAt serves an alborz whose -public-url is public, with upstreams that may name its own host.
func serveAlborzAt(t *testing.T, upstreams func(host string) []string, cache bool, public *url.URL) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	newAlborz(t, e, &alborz.Options{
		Upstreams: upstreams(ln.Addr().String()),
		LoginKey:  smokeKey,
		DataDir:   t.TempDir(),
		Cache:     cache,
		PublicURL: public,
		// A handler that sends the reader elsewhere says what it did.
		StrictNotices: true,
	})
	go e.Server.Serve(ln)
	t.Cleanup(func() { e.Close() })
	return "http://" + ln.Addr().String()
}

// A page that 500s is invisible to every test that does not open it.
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
	// Without the attachment row, the checks below pass without having run.
	if !strings.Contains(body, "mailbox.attachment") && !strings.Contains(body, "message-mark") {
		t.Fatalf("the seeded attachment leaves no mark on its row; that row is unchecked")
	}

	nested := get(t, c, base+"/mailbox/INBOX%2FLists")
	nestedUIDs := messageUIDs(nested)
	if len(nestedUIDs) == 0 {
		t.Fatal("the nested folder shows no messages; the escaping is unchecked")
	}
	var nestedParts []string
	for _, uid := range nestedUIDs {
		page := get(t, c, base+"/message/INBOX%2FLists/"+uid)
		nestedParts = append(nestedParts, rawPartHrefs(page)...)
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

// The escaping under test is the page's, not the test's.
func rawPartHrefs(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(
		`href="(/message/[^"]*/raw\?[^"]*)"`).FindAllStringSubmatch(body, -1) {
		out = append(out, strings.ReplaceAll(m[1], "&amp;", "&"))
	}
	return out
}

// The srcdoc arrives attribute-escaped.
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
	return loginAs(t, base, smokeUser)
}

func loginAs(t *testing.T, base, account string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	resp, err := c.PostForm(base+"/login",
		url.Values{"username": {account}, "password": {smokePass}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resp.Body.Close()
	return c
}

// The login must still work after answering the bad query.
func TestUnknownSearchKeySurvives(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)

	if body := get(t, c, base+"/mailbox/INBOX?query=foo:bar"); body == "" {
		t.Fatal("the unrecognised key produced no page")
	}
	if body := get(t, c, base+"/mailbox/INBOX"); !strings.Contains(body, "message-row") {
		t.Fatal("the login stopped serving mail after an unrecognised search key")
	}
}

// The prefetch runs behind the listing, so the sequence repeats to meet a held body.
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
		t.Fatal("the login stopped serving mail after the flag change")
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

// Redirecting to whatever composeFromMailto returns would forward the reader anywhere.
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

	// Otherwise the check above passes by refusing everything.
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

func TestExportFailsAsAnError(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)

	body := get(t, c, base+"/mailbox/INBOX")
	uids := messageUIDs(body)
	if len(uids) < 2 {
		t.Fatalf("need two seeded messages to export, found %d", len(uids))
	}

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

	// Otherwise the checks above pass by refusing everything.
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

	// A field per message is megabytes on a large folder.
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

// An export is the reader's copy of their mail: every message whole, in order.
func TestExportHoldsEveryMessageWhole(t *testing.T) {
	addr, _ := startIMAPServer(t, rigCaps())
	ic, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ic.Close()
	if err := ic.Login(smokeUser, smokePass).Wait(); err != nil {
		t.Fatal(err)
	}
	file := make([]byte, 3<<20)
	for i := range file {
		file[i] = byte(i * 7)
	}
	encoded := base64.StdEncoding.EncodeToString(file)
	var wrapped strings.Builder
	for len(encoded) > 76 {
		wrapped.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	wrapped.WriteString(encoded + "\r\n")
	var sent []string
	for i := range 3 {
		raw := fmt.Sprintf("From: <eve@example.org>\r\nTo: <%s>\r\nSubject: Export %d\r\nMessage-ID: <export-%d@test>\r\n"+
			"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b\"\r\n\r\n"+
			"--b\r\nContent-Type: text/plain\r\n\r\nFrom the start of a line.\r\n>From quoted.\r\n"+
			"--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n"+
			"Content-Disposition: attachment; filename=\"file%d.bin\"\r\n\r\n%s--b--\r\n", smokeUser, i, i, i, wrapped.String())
		cmd := ic.Append("INBOX", int64(len(raw)), nil)
		cmd.Write([]byte(raw))
		cmd.Close()
		if _, err := cmd.Wait(); err != nil {
			t.Fatalf("append: %v", err)
		}
		sent = append(sent, raw)
	}

	base := startAlborz(t, addr)
	c := login(t, base)
	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX?query="+url.QueryEscape("subject:Export")))
	if len(uids) != len(sent) {
		t.Fatalf("the search finds %d of the %d messages", len(uids), len(sent))
	}
	resp, err := c.PostForm(base+"/message/INBOX/export", url.Values{"uids": uids})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export: %s", resp.Status)
	}
	archive := mbox.NewReader(resp.Body)
	archive.Mboxrd = true
	var got []string
	for {
		r, err := archive.NextMessage()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading the export: %v", err)
		}
		b, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("reading the export: %v", err)
		}
		got = append(got, string(b))
	}
	if len(got) != len(sent) {
		t.Fatalf("the export holds %d messages, want %d", len(got), len(sent))
	}
	for _, raw := range sent {
		if !slices.Contains(got, raw) {
			t.Errorf("no exported message is the one sent as %q", headOf(raw))
		}
	}
}

// A compressor that miscounts the write crossing its threshold breaks a
// download that reaches it in small writes.
func TestGzippedDownloadArrivesWhole(t *testing.T) {
	addr, _ := startIMAPServer(t, rigCaps())
	ic, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ic.Close()
	if err := ic.Login(smokeUser, smokePass).Wait(); err != nil {
		t.Fatal(err)
	}
	raw := "From: <eve@example.org>\r\nTo: <" + smokeUser + ">\r\nSubject: Gzipped\r\n\r\n" +
		strings.Repeat("A line of a long message.\r\n", 2000)
	cmd := ic.Append("INBOX", int64(len(raw)), nil)
	cmd.Write([]byte(raw))
	cmd.Close()
	appended, err := cmd.Wait()
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	// Trickled, the message reaches the response in writes below the threshold.
	base := startAlborz(t, trickleProxy(t, addr, 512))
	c := login(t, base)
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/message/INBOX/%d/eml", base, appended.UID), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Set by hand, the header turns off the transport's own decoding.
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download: %s", resp.Status)
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("the download was not compressed: Content-Encoding %q", ce)
	}
	zr, err := gzip.NewReader(resp.Body)
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

// A download cut off by the server must fail in the browser, not arrive
// as a shorter file that looks whole.
func TestBrokenDownloadIsNotComplete(t *testing.T) {
	file := make([]byte, 3<<20)
	for i := range file {
		file[i] = byte(i * 7)
	}
	encoded := base64.StdEncoding.EncodeToString(file)
	var wrapped strings.Builder
	for len(encoded) > 76 {
		wrapped.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	wrapped.WriteString(encoded + "\r\n")
	raw := "From: <eve@example.org>\r\nTo: <" + smokeUser + ">\r\nSubject: Cut off\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nSee the file.\r\n" +
		"--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"file.bin\"\r\n\r\n" + wrapped.String() + "--b--\r\n"

	for name, download := range map[string]func(c *http.Client, base string, uid imap.UID) (*http.Response, error){
		"mbox": func(c *http.Client, base string, uid imap.UID) (*http.Response, error) {
			return c.PostForm(base+"/message/INBOX/export", url.Values{"uids": {fmt.Sprint(uid)}})
		},
		"zip": func(c *http.Client, base string, uid imap.UID) (*http.Response, error) {
			return c.Get(fmt.Sprintf("%s/message/INBOX/%d/attachments.zip", base, uid))
		},
	} {
		t.Run(name, func(t *testing.T) {
			addr, _ := startIMAPServer(t, rigCaps())
			ic, err := imapclient.DialInsecure(addr, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ic.Close()
			if err := ic.Login(smokeUser, smokePass).Wait(); err != nil {
				t.Fatal(err)
			}
			cmd := ic.Append("INBOX", int64(len(raw)), nil)
			cmd.Write([]byte(raw))
			cmd.Close()
			appended, err := cmd.Wait()
			if err != nil {
				t.Fatalf("append: %v", err)
			}
			proxy, arm := cutProxy(t, addr, 1<<20)
			base := startAlborz(t, proxy)
			c := login(t, base)
			arm()
			// Held back by compression, the cut may come before the response does.
			resp, err := download(c, base, appended.UID)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if got, err := io.ReadAll(resp.Body); err == nil {
				t.Errorf("a download cut off after %d bytes arrived as complete: %s", len(got), resp.Status)
			}
		})
	}
}

// proxy passes IMAP traffic through, the server's side by relay.
func proxy(t *testing.T, upstream string, relay func(client, server net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", upstream)
			if err != nil {
				client.Close()
				continue
			}
			go func() {
				io.Copy(server, client)
				server.Close()
			}()
			go func() {
				defer client.Close()
				defer server.Close()
				relay(client, server)
			}()
		}
	}()
	return ln.Addr().String()
}

// cutProxy drops, once armed, the connection on which the server has sent
// limit more bytes.
func cutProxy(t *testing.T, upstream string, limit int64) (string, func()) {
	t.Helper()
	armed := make(chan struct{})
	addr := proxy(t, upstream, func(client, server net.Conn) {
		buf := make([]byte, 32<<10)
		var sent int64
		for {
			n, err := server.Read(buf)
			select {
			case <-armed:
				if sent += int64(n); sent > limit {
					return
				}
			default:
			}
			if _, werr := client.Write(buf[:n]); werr != nil || err != nil {
				return
			}
		}
	})
	return addr, func() { close(armed) }
}

// trickleProxy passes the server's bytes in pieces of at most chunk bytes,
// each arriving on its own.
func trickleProxy(t *testing.T, upstream string, chunk int) string {
	t.Helper()
	return proxy(t, upstream, func(client, server net.Conn) {
		buf := make([]byte, chunk)
		for {
			n, err := server.Read(buf)
			if _, werr := client.Write(buf[:n]); werr != nil || err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
}

// It asserts the bytes sent to the SMTP server, the only place the answer is visible.
func TestSendHTMLFollowsWhatWasWritten(t *testing.T) {
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

	compose := func() string { return get(t, c, base+"/compose") }

	sendFrom(t, c, base, "/compose", compose(), "text", "Hello.", "html", "<p>Hello.</p>")
	if !strings.Contains(sent.Last(), "text/html") {
		t.Errorf("a message written in the editor went out without its HTML:\n%s", headOf(sent.Last()))
	}

	sendFrom(t, c, base, "/compose", compose(), "text", "Hello.\n\nEnglish only.")
	if strings.Contains(sent.Last(), "multipart/alternative") {
		t.Errorf("left-to-right plain text was sent with an HTML part:\n%s", headOf(sent.Last()))
	}

	sendFrom(t, c, base, "/compose", compose())
	if !strings.Contains(sent.Last(), "multipart/alternative") {
		t.Errorf("right-to-left plain text was sent with no direction:\n%s", headOf(sent.Last()))
	}

	path := "/message/INBOX/" + listUID + "/reply?part=1"
	sendFrom(t, c, base, path, get(t, c, base+path))
	if strings.Contains(sent.Last(), "multipart/alternative") {
		t.Errorf("a reply to list mail carried an HTML part:\n%s", headOf(sent.Last()))
	}

	// An unticked box is absent from the form.
	const ticked = `name="direction_part" id="direction_part" value="1" checked`
	if !strings.Contains(get(t, c, base+"/settings"), ticked) {
		t.Fatal("the direction part is not on by default")
	}
	resp := postForm(t, c, base+"/settings", url.Values{"reply_position": {"above"}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("saving settings: %s", resp.Status)
	}
	if strings.Contains(get(t, c, base+"/settings"), ticked) {
		t.Fatal("the settings page still shows the direction part on")
	}
	sendFrom(t, c, base, "/compose", compose(), "subject", "turned off")
	if !strings.Contains(sent.Last(), "Subject: turned off") {
		t.Fatalf("the message sent with the part turned off never arrived:\n%s", headOf(sent.Last()))
	}
	if strings.Contains(sent.Last(), "text/html") {
		t.Errorf("right-to-left plain text carried an HTML part the writer turned off:\n%s", headOf(sent.Last()))
	}
}

// The draft is the URL's account's; From only says who sends.
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

func TestReopenedDraftKeepsItsLines(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	text := strings.Repeat("A paragraph long enough to be wrapped when saved. ", 4) + "End."
	sendFrom(t, c, base, "/compose", get(t, c, base+"/compose"), "save_as_draft", "1", "text", text)
	drafts := messageUIDs(get(t, c, base+"/mailbox/Drafts"))
	if len(drafts) != 1 {
		t.Fatalf("want one draft, found %d", len(drafts))
	}
	page := get(t, c, base+"/message/Drafts/"+drafts[0]+"/edit?part=1")
	if got := html.UnescapeString(between(page, `data-caret="end">`, "</textarea>")); got != text {
		t.Errorf("the reopened draft reads\n%q\nwant\n%q", got, text)
	}
}

// Another client's deleted message is its to expunge, never ours.
func TestSavingADraftLeavesOthersDeletedMessages(t *testing.T) {
	withoutUIDPlus := rigCaps()
	delete(withoutUIDPlus, imap.CapIMAP4rev2)
	for name, caps := range map[string]imap.CapSet{"uidplus": rigCaps(), "without uidplus": withoutUIDPlus} {
		t.Run(name, func(t *testing.T) {
			addr, _ := startIMAPServer(t, caps)
			base := startAlborz(t, addr)
			c := login(t, base)

			ic, err := imapclient.DialInsecure(addr, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ic.Close()
			if err := ic.Login(smokeUser, smokePass).Wait(); err != nil {
				t.Fatal(err)
			}
			theirs := "From: <eve@example.org>\r\nSubject: discarded elsewhere\r\n\r\nGone later.\r\n"
			cmd := ic.Append("Drafts", int64(len(theirs)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagDeleted}})
			cmd.Write([]byte(theirs))
			cmd.Close()
			if _, err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			drafts := func(criteria *imap.SearchCriteria) []imap.UID {
				t.Helper()
				if _, err := ic.Select("Drafts", nil).Wait(); err != nil {
					t.Fatal(err)
				}
				found, err := ic.UIDSearch(criteria, nil).Wait()
				if err != nil {
					t.Fatal(err)
				}
				return found.AllUIDs()
			}
			ours := &imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: "direction"}}}

			sendFrom(t, c, base, "/compose", get(t, c, base+"/compose"), "save_as_draft", "1")
			first := drafts(ours)
			if len(first) != 1 {
				t.Fatalf("want the saved draft, found %d", len(first))
			}
			path := fmt.Sprintf("/message/Drafts/%d/edit?part=1", first[0])
			sendFrom(t, c, base, path, get(t, c, base+path), "save_as_draft", "1")

			if n := len(drafts(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: "discarded elsewhere"}}})); n != 1 {
				t.Errorf("another client's deleted message was expunged")
			}
			if n := len(drafts(&imap.SearchCriteria{Header: ours.Header, NotFlag: []imap.Flag{imap.FlagDeleted}})); n != 1 {
				t.Errorf("want one live draft after saving again, found %d", n)
			}
			replaced := drafts(&imap.SearchCriteria{UID: []imap.UIDSet{imap.UIDSetNum(first[0])}, Flag: []imap.Flag{imap.FlagDeleted}})
			// Without UIDPLUS only a plain EXPUNGE could remove it, and that takes the others too.
			want := !caps.Has(imap.CapUIDPlus)
			if left := len(replaced) == 1; left != want {
				t.Errorf("replaced draft left marked deleted: %v, want %v", left, want)
			}
		})
	}
}

// Both read the message only to decide; opening it is what marks it read.
func TestUnsubscribeAndAddToCalendarLeaveTheMessageUnread(t *testing.T) {
	addr, _ := startIMAPServer(t, rigCaps())
	base := startAlborz(t, addr)
	c := login(t, base)

	ic, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ic.Close()
	if err := ic.Login(smokeUser, smokePass).Wait(); err != nil {
		t.Fatal(err)
	}
	invite := "From: <eve@example.org>\r\nTo: <" + smokeUser + ">\r\nSubject: Planning\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/calendar; charset=utf-8; method=REQUEST\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//rig//EN\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:planning@rig\r\nDTSTAMP:20260901T090000Z\r\nDTSTART:20261001T090000Z\r\n" +
		"SUMMARY:Planning\r\nORGANIZER:mailto:eve@example.org\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cmd := ic.Append("INBOX", int64(len(invite)), nil)
	cmd.Write([]byte(invite))
	cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := ic.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	uidOf := func(subject string) imap.UID {
		t.Helper()
		found, err := ic.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: subject}}}, nil).Wait()
		if err != nil {
			t.Fatal(err)
		}
		uids := found.AllUIDs()
		if len(uids) != 1 {
			t.Fatalf("want one message about %q, found %d", subject, len(uids))
		}
		return uids[0]
	}

	for _, action := range []struct{ subject, path string }{
		{"Planning", "/calendar/from-message?mbox=INBOX&uid=%d"},
		{"Ideas for the redesign", "/message/INBOX/%d/unsubscribe"},
	} {
		uid := uidOf(action.subject)
		path := fmt.Sprintf(action.path, uid)
		resp, err := c.Post(base+path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		msgs, err := ic.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{Flags: true}).Collect()
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 {
			t.Fatalf("%s: want the message back, got %d", path, len(msgs))
		}
		if slices.Contains(msgs[0].Flags, imap.FlagSeen) {
			t.Errorf("%s marked the message read", path)
		}
	}
}

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

func TestPastedImageTravelsBesideTheHTML(t *testing.T) {
	smtpAddr, sent := startSMTP(t)
	base := startAlborz(t, startIMAP(t), smtpAddr)
	c := login(t, base)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="attachments"; filename="shot.png"`)
	h.Set("Content-Type", "image/png")
	fw, err := w.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte("\x89PNG\r\n\x1a\nnot really"))
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

	sendFrom(t, c, base, "/compose", get(t, c, base+"/compose"), "subject", "pasted",
		"html", `<p>Look: <img src="/compose/attachment/`+ids[0]+`" alt="shot.png"></p>`)
	got := sent.Last()
	if !strings.Contains(got, "Subject: pasted") {
		t.Fatalf("the message was not sent:\n%s", headOf(got))
	}

	entity, err := message.Read(strings.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	var (
		htmlBody, cid string
		related       bool
		files         int
	)
	entity.Walk(func(_ []int, part *message.Entity, err error) error {
		if err != nil {
			return err
		}
		mediaType, _, _ := part.Header.ContentType()
		disposition, _, _ := part.Header.ContentDisposition()
		switch {
		case mediaType == "multipart/related":
			related = true
		case mediaType == "text/html":
			b, _ := io.ReadAll(part.Body)
			htmlBody = string(b)
		case mediaType == "image/png" && disposition == "inline":
			cid = strings.Trim(part.Header.Get("Content-Id"), "<>")
		}
		if disposition == "attachment" {
			files++
		}
		return nil
	})
	if !related || cid == "" {
		t.Fatalf("no image beside the HTML (related %v, Content-ID %q):\n%s", related, cid, headOf(got))
	}
	if !strings.Contains(htmlBody, `src="cid:`+cid+`"`) {
		t.Errorf("the HTML does not name the image by its Content-ID %q:\n%s", cid, htmlBody)
	}
	if files != 0 {
		t.Errorf("the image also left as %d file(s)", files)
	}
}

func TestDoneNoticeClosesWithoutAScript(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	postForm(t, c, base+"/message/INBOX/move?to=Trash", url.Values{"uids": uids[:1]})
	notice := noticeBar(get(t, c, base+"/mailbox/INBOX"))
	if !strings.Contains(notice, "notice-done") {
		t.Fatalf("no done notice after a move: %q", notice)
	}
	if !strings.Contains(notice, `<form method="dialog" class="notice-dismiss">`) {
		t.Errorf("the notice has no cross that closes it without a script:\n%s", notice)
	}
}

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
			// The image belongs to the HTML alternative, not the chosen text.
			"alternative", nil, "Body text.", "1", nil, []string{`class="shown-image"`}},
		{"text and the image it shows itself", []string{text,
			"Content-Type: image/png; name=\"logo.png\"\r\nContent-ID: <logo>\r\n" + pdf},
			// Named by no file list, so the page shows it rather than losing it.
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

// The page depends on the error keeping its type through every wrap.
func TestUpstreamOutageIsNotOurFailure(t *testing.T) {
	addr, srv := startIMAPServer(t, rigCaps())
	base := startAlborz(t, addr)
	c := login(t, base)
	uids := messageUIDs(get(t, c, base+"/mailbox/INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}

	// The reader's goroutine notices the loss a moment after Close returns.
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

func TestNewestFirstWithoutSort(t *testing.T) {
	caps := rigCaps()
	delete(caps, imap.CapSort)
	addr, _ := startIMAPServer(t, caps)
	base := startAlborz(t, addr)
	c := login(t, base)

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

func TestDatesPastTheSolarHijriReckoningShowGregorian(t *testing.T) {
	imapAddr := startIMAP(t)
	base := startAlborz(t, imapAddr)
	c := login(t, base)
	dates := map[string]string{
		"Mon, 1 Jan 4000 10:00:00 +0000": "January 1, 4000",
		"Fri, 1 Jan 0560 10:00:00 +0000": "January 1, 560",
	}
	var uids []string
	for date := range dates {
		uids = append(uids, appendMessage(t, imapAddr, "INBOX", "From: a@example.org\r\nTo: "+smokeUser+
			"\r\nSubject: Dated\r\nDate: "+date+"\r\n\r\n"))
	}
	for _, reading := range []url.Values{
		{"calendar": {"shcal"}, "secondary": {""}},
		{"calendar": {"gregorian"}, "secondary": {"shcal"}},
	} {
		postForm(t, c, base+"/settings", reading)
		list := get(t, c, base+"/mailbox/INBOX")
		for _, uid := range uids {
			get(t, c, base+"/message/INBOX/"+uid)
		}
		for _, shown := range dates {
			if !strings.Contains(list, shown) {
				t.Errorf("with %v the list does not show %q", reading, shown)
			}
		}
	}
}

// A date is read in the calendar its field names: typed Solar Hijri beyond the reckoning is refused, a stored date beyond it round-trips in Gregorian.
func TestInputPastTheSolarHijriReckoning(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)
	coll := strings.TrimPrefix(createCalendar(t, base, c, "Plans", "plans"), base)
	put(t, base, coll+"far.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:far\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:40000101T100000Z\r\nDTEND:40000101T110000Z\r\n"+
		"SUMMARY:Far\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	edit := base + "/calendar/" + url.PathEscape(hereAt+coll+"far.ics") + "/update"
	saved := func() string {
		_, body := dav(t, http.MethodGet, base+coll+"far.ics", nil, "")
		return body
	}

	postForm(t, c, base+"/settings", url.Values{"calendar": {"shcal"}, "secondary": {""}})
	for _, month := range []string{"3178-01", "-62-01"} {
		resp, err := c.Get(base + "/calendar?month=" + month)
		if err != nil {
			t.Fatalf("month %s: %v", month, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("month %s answered %s, want it refused", month, resp.Status)
		}
	}
	typed := url.Values{"summary": {"Typed"}, "calendar": {smokeUser + "|" + hereAt + coll},
		"start": {"3178/01/01 10:00"}, "end": {"3178/01/01 11:00"}, "start-calendar": {"shcal"}, "end-calendar": {"shcal"}}
	if resp := postForm(t, c, base+"/calendar/create", typed); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a typed year past the reckoning answered %s, want the form refused", resp.Status)
	}

	page := get(t, c, edit)
	shown := url.Values{"summary": {"Far again"}}
	for _, name := range []string{"start", "end", "start-calendar", "end-calendar"} {
		field := regexp.MustCompile(`name="` + name + `"[^>]*value="([^"]*)"`).FindStringSubmatch(page)
		if field == nil {
			t.Fatalf("the edit form has no %s", name)
		}
		shown.Set(name, field[1])
	}
	if shown.Get("start") != "4000-01-01T10:00" || shown.Get("start-calendar") != "gregorian" {
		t.Fatalf("the edit form holds the stored start as %v, want it in Gregorian", shown)
	}
	if resp := postForm(t, c, edit, shown); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the form as shown answered %s", resp.Status)
	}
	if body := saved(); !strings.Contains(body, "DTSTART:40000101T100000Z") || !strings.Contains(body, "Far again") {
		t.Errorf("the saved event moved: %s", body)
	}

	early := url.Values{"summary": {"Early"}, "allday": {"on"},
		"start_date": {"0100-01-01"}, "end_date": {"0100-01-01"}, "start_date-calendar": {"shcal"}, "end_date-calendar": {"shcal"}}
	if resp := postForm(t, c, edit, early); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving Solar Hijri 0100-01-01 answered %s", resp.Status)
	}
	// 1 Farvardin 100 falls in March 721.
	if body := saved(); !strings.Contains(body, "DTSTART;VALUE=DATE:0721032") {
		t.Errorf("Solar Hijri 0100-01-01 was not saved as a day of Nowruz 721: %s", body)
	}

	postForm(t, c, base+"/settings", url.Values{"calendar": {"gregorian"}, "secondary": {"shcal"}})
	get(t, c, base+"/calendar?month=4000-01")
}

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
	if page := get(t, c, base+"/compose"); !strings.Contains(page, `value="`+smokeUser+`" selected`) {
		t.Errorf("compose without an account does not select the first listed one")
	}
}

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

// The server decodes a part where it speaks BINARY, and alborz on the way out where it does not.
func TestDownloadedPartIsTheFileSent(t *testing.T) {
	file := bytes.Repeat([]byte("alborz\x00\xff\r\n"), 1<<16)
	encoded := base64.StdEncoding.EncodeToString(file)
	var wrapped strings.Builder
	for len(encoded) > 76 {
		wrapped.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	wrapped.WriteString(encoded + "\r\n")
	raw := "From: <eve@example.org>\r\nTo: <" + smokeUser + ">\r\nSubject: A file\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nSee the file.\r\n" +
		"--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"file.bin\"\r\n\r\n" + wrapped.String() + "--b--\r\n"

	for name, caps := range map[string]imap.CapSet{"BINARY": rigCaps(), "IMAP4rev1": {imap.CapIMAP4rev1: {}}} {
		t.Run(name, func(t *testing.T) {
			addr, _ := startIMAPServer(t, caps)
			ic, err := imapclient.DialInsecure(addr, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ic.Close()
			if err := ic.Login(smokeUser, smokePass).Wait(); err != nil {
				t.Fatal(err)
			}
			cmd := ic.Append("INBOX", int64(len(raw)), nil)
			cmd.Write([]byte(raw))
			cmd.Close()
			appended, err := cmd.Wait()
			if err != nil || appended.UID == 0 {
				t.Fatalf("append: %v", err)
			}

			base := startAlborz(t, addr)
			c := login(t, base)
			resp, err := c.Get(fmt.Sprintf("%s/message/INBOX/%d/raw?part=2", base, appended.UID))
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !bytes.Equal(got, file) {
				t.Fatalf("%s: got %d bytes, want the %d sent", resp.Status, len(got), len(file))
			}
			if caps.Has(imap.CapBinary) && resp.ContentLength != int64(len(file)) {
				t.Errorf("the length the server knew was not passed on: %d", resp.ContentLength)
			}
		})
	}
}

// A forward carries the original's file and the original itself, and what
// leaves is, byte for byte, what the Sent folder keeps.
func TestForwardLeavesAsTheSentCopyKeepsIt(t *testing.T) {
	file := make([]byte, 8<<20)
	rand.NewChaCha8([32]byte{}).Read(file)
	var original bytes.Buffer
	var h message.Header
	h.Set("From", "<eve@example.org>")
	h.Set("To", "<"+smokeUser+">")
	h.Set("Subject", "A large file")
	h.Set("MIME-Version", "1.0")
	h.Set("Content-Type", "multipart/mixed")
	mw, err := message.CreateWriter(&original, h)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []struct {
		header map[string]string
		body   []byte
	}{
		{map[string]string{"Content-Type": "text/plain"}, []byte("See the file.\r\n")},
		{map[string]string{
			"Content-Type":              "application/octet-stream",
			"Content-Transfer-Encoding": "base64",
			"Content-Disposition":       `attachment; filename="file.bin"`,
		}, file},
	} {
		var ph message.Header
		for k, v := range part.header {
			ph.Set(k, v)
		}
		w, err := mw.CreatePart(ph)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(part.body)
		w.Close()
	}
	mw.Close()

	imapAddr := startIMAP(t)
	ic, err := imapclient.DialInsecure(imapAddr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ic.Close()
	if err := ic.Login(smokeUser, smokePass).Wait(); err != nil {
		t.Fatal(err)
	}
	cmd := ic.Append("INBOX", int64(original.Len()), nil)
	cmd.Write(original.Bytes())
	cmd.Close()
	appended, err := cmd.Wait()
	if err != nil || appended.UID == 0 {
		t.Fatalf("append: %v", err)
	}

	smtpAddr, sent := startSMTP(t)
	base := startAlborz(t, imapAddr, smtpAddr)
	c := login(t, base)
	path := fmt.Sprintf("/message/INBOX/%d/forward?part=1", appended.UID)
	page := get(t, c, base+path)
	if !strings.Contains(page, `name="prev_attachments" value="2"`) {
		t.Fatal("the forward does not offer the original's file")
	}
	sendFrom(t, c, base, path, page, "prev_attachments", "2",
		"attached_messages", fmt.Sprintf("INBOX/%d", appended.UID))
	out := sent.Last()
	if len(out) < 2*len(file) {
		t.Fatalf("%d bytes left, fewer than the file and the original weigh", len(out))
	}

	if stored := sentCopy(t, imapAddr); stored != out {
		t.Fatalf("the Sent copy (%d bytes) is not what left (%d bytes)", len(stored), len(out))
	}

	e, err := message.Read(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	var carried []string
	err = e.Walk(func(_ []int, part *message.Entity, err error) error {
		if err != nil {
			return err
		}
		mediaType, _, _ := part.Header.ContentType()
		_, params, _ := part.Header.ContentDisposition()
		var want []byte
		switch {
		case mediaType == "message/rfc822":
			want = original.Bytes()
		case params["filename"] == "file.bin":
			want = file
		default:
			return nil
		}
		got, err := io.ReadAll(part.Body)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s arrived as %d bytes, not the %d it has", mediaType, len(got), len(want))
		}
		carried = append(carried, mediaType)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(carried) != 2 {
		t.Errorf("the forward carried %v, want the file and the original", carried)
	}
}

// An image the original shows travels with a rich forward, fetched as the
// message is written.
func TestForwardedImageTravelsBesideTheHTML(t *testing.T) {
	image := make([]byte, 1<<20)
	rand.NewChaCha8([32]byte{1}).Read(image)
	var original bytes.Buffer
	var h message.Header
	h.Set("From", "<eve@example.org>")
	h.Set("To", "<"+smokeUser+">")
	h.Set("Subject", "A picture")
	h.Set("MIME-Version", "1.0")
	h.Set("Content-Type", `multipart/related; type="text/html"`)
	mw, err := message.CreateWriter(&original, h)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []struct {
		header map[string]string
		body   []byte
	}{
		{map[string]string{"Content-Type": "text/html; charset=utf-8"}, []byte(`<p>Look: <img src="cid:pic@example.org"></p>`)},
		{map[string]string{
			"Content-Type":              "image/png",
			"Content-Transfer-Encoding": "base64",
			"Content-Id":                "<pic@example.org>",
			"Content-Disposition":       `inline; filename="pic.png"`,
		}, image},
	} {
		var ph message.Header
		for k, v := range part.header {
			ph.Set(k, v)
		}
		w, err := mw.CreatePart(ph)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(part.body)
		w.Close()
	}
	mw.Close()

	imapAddr := startIMAP(t)
	uid := appendMessage(t, imapAddr, "INBOX", original.String())
	smtpAddr, sent := startSMTP(t)
	base := startAlborz(t, imapAddr, smtpAddr)
	c := login(t, base)
	path := "/message/INBOX/" + uid + "/forward?part=1"
	page := get(t, c, base+path)
	src := html.UnescapeString(between(page, `<img src="`, `"`))
	if !strings.HasSuffix(src, "part=2") {
		t.Fatalf("the forward does not show the original's image: %q", src)
	}
	sendFrom(t, c, base, path, page, "html", `<p>Look:</p><p><img src="`+html.EscapeString(src)+`"></p>`)

	e, err := message.Read(strings.NewReader(sent.Last()))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	e.Walk(func(_ []int, part *message.Entity, err error) error {
		if err != nil {
			return err
		}
		if mediaType, _, _ := part.Header.ContentType(); mediaType != "image/png" {
			return nil
		}
		found = true
		got, _ := io.ReadAll(part.Body)
		if !bytes.Equal(got, image) {
			t.Errorf("the image arrived as %d bytes, not the %d it has", len(got), len(image))
		}
		return nil
	})
	if !found {
		t.Fatalf("the forward left without the image:\n%s", headOf(sent.Last()))
	}
}

// A message attached whole is never encoded (RFC 2046 5.2.1); one the way
// out cannot carry as it is goes as a file, its bytes intact.
func TestAttachedMessageTravelsAsItsBytesAllow(t *testing.T) {
	long := strings.Repeat("x", 998)
	head := "From: <eve@example.org>\r\nTo: <" + smokeUser + ">\r\nSubject: Whole\r\nMIME-Version: 1.0\r\n"
	sevenBit := head + "Content-Type: text/plain\r\n\r\nPlain.\r\n" + long + "\r\n"
	eightBit := head + "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\nسلام\r\n" + long + "\r\n"
	binary := head + "Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n\r\na\x00b\r\n"
	for _, c := range []struct {
		name, original string
		hidden         []string
		mediaType, cte string
	}{
		{"7bit", sevenBit, nil, "message/rfc822", "7bit"},
		{"8bit", eightBit, nil, "message/rfc822", "8bit"},
		{"8bit without 8BITMIME", eightBit, []string{"8BITMIME"}, "application/octet-stream", "base64"},
		{"binary", binary, nil, "application/octet-stream", "base64"},
	} {
		t.Run(c.name, func(t *testing.T) {
			imapAddr := startIMAP(t)
			uid := appendMessage(t, imapAddr, "INBOX", c.original)
			smtpAddr, sent := startSMTP(t, c.hidden...)
			base := startAlborz(t, imapAddr, smtpAddr)
			cl := login(t, base)
			sendFrom(t, cl, base, "/compose", get(t, cl, base+"/compose"), "attached_messages", "INBOX/"+uid)
			out := sent.Last()
			if out == "" {
				t.Fatal("nothing left")
			}
			if n := sent.Sessions(); n != 1 {
				t.Errorf("the send took %d SMTP sessions, want one", n)
			}

			e, err := message.Read(strings.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			e.Walk(func(_ []int, part *message.Entity, err error) error {
				if err != nil {
					return err
				}
				if disposition, _, _ := part.Header.ContentDisposition(); disposition != "attachment" {
					return nil
				}
				found = true
				mediaType, _, _ := part.Header.ContentType()
				if cte := part.Header.Get("Content-Transfer-Encoding"); mediaType != c.mediaType || cte != c.cte {
					t.Errorf("the message went as %s, %s; want %s, %s", mediaType, cte, c.mediaType, c.cte)
				}
				if got, _ := io.ReadAll(part.Body); string(got) != c.original {
					t.Errorf("the message arrived as %q", got)
				}
				return nil
			})
			if !found {
				t.Fatalf("the message left without the one attached:\n%s", headOf(out))
			}
			if kept := sentCopy(t, imapAddr); kept != out {
				t.Errorf("the Sent copy (%d bytes) is not what left (%d bytes)", len(kept), len(out))
			}
		})
	}
}

// A whole message the mail server cannot hand over is not the SMTP server's
// refusal; the form comes back saying which it was.
func TestUnreadableOriginalIsNotASendRefusal(t *testing.T) {
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(smokeUser, smokePass)
	for _, name := range []string{"INBOX", "Sent"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	raw := "From: <eve@example.org>\r\nSubject: Gone\r\n\r\nBody.\r\n"
	appended, err := user.Append("INBOX", literal{strings.NewReader(raw), int64(len(raw))}, &imap.AppendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return wholeFetchFails{mem.NewSession()}, nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
	})
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	smtpAddr, sent := startSMTP(t)
	base := startAlborz(t, ln.Addr().String(), smtpAddr)
	c := login(t, base)
	resp := postCompose(t, c, base, "/compose", get(t, c, base+"/compose"),
		"attached_messages", fmt.Sprintf("INBOX/%d", appended.UID))
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unreadable original answered %s, want 422", resp.Status)
	}
	notice := noticeBar(string(page))
	if !strings.Contains(notice, "could not be read from the mail server") || strings.Contains(notice, "was not sent. The server said") {
		t.Errorf("the form does not say the original was unreadable:\n%s", notice)
	}
	if sent.Sessions() != 0 {
		t.Errorf("the SMTP server was contacted for a message that could not be written")
	}
}

// wholeFetchFails refuses to hand over a whole message, as a server might
// when the message went meanwhile.
type wholeFetchFails struct{ imapserver.Session }

func (s wholeFetchFails) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	for _, section := range options.BodySection {
		if section.Specifier == imap.PartSpecifierNone && len(section.Part) == 0 {
			return errors.New("the message is gone")
		}
	}
	return s.Session.Fetch(w, numSet, options)
}

func sentCopy(t *testing.T, imapAddr string) string {
	t.Helper()
	ic, err := imapclient.DialInsecure(imapAddr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ic.Close()
	if err := ic.Login(smokeUser, smokePass).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := ic.Select("Sent", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	section := &imap.FetchItemBodySection{Peek: true}
	kept, err := ic.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil || len(kept) != 1 {
		t.Fatalf("the Sent copy: %v", err)
	}
	return string(kept[0].FindBodySection(section))
}

// Opening a signed message reads only the part it shows; the verdict, which
// streams the signed part through the check, comes from a request of its own.
func TestSignedMessageOpensWithoutItsAttachment(t *testing.T) {
	key, err := openpgp.NewEntity("Gil", "", "gil@example.org", nil)
	if err != nil {
		t.Fatal(err)
	}
	var public bytes.Buffer
	if err := key.Serialize(&public); err != nil {
		t.Fatal(err)
	}
	attachment := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xa5}, 1<<21))
	var wrapped strings.Builder
	for len(attachment) > 76 {
		wrapped.WriteString(attachment[:76] + "\r\n")
		attachment = attachment[76:]
	}
	signed := "Content-Type: multipart/mixed; boundary=in\r\n\r\n" +
		"--in\r\nContent-Type: text/plain\r\n\r\nThe signed words.\r\n" +
		"--in\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"big.bin\"\r\n\r\n" + wrapped.String() + attachment + "\r\n--in--\r\n"
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, key, strings.NewReader(signed), nil); err != nil {
		t.Fatal(err)
	}
	raw := "From: Gil <gil@example.org>\r\nTo: <" + smokeUser + ">\r\nSubject: Signed and large\r\nMIME-Version: 1.0\r\n" +
		"Autocrypt: addr=gil@example.org; keydata=" + base64.StdEncoding.EncodeToString(public.Bytes()) + "\r\n" +
		"Content-Type: multipart/signed; micalg=pgp-sha256; protocol=\"application/pgp-signature\"; boundary=sig\r\n\r\n" +
		"--sig\r\n" + signed + "\r\n--sig\r\nContent-Type: application/pgp-signature\r\n\r\n" + sig.String() + "\r\n--sig--\r\n"

	mem := imapmemserver.New()
	user := imapmemserver.NewUser(smokeUser, smokePass)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	appended, err := user.Append("INBOX", strings.NewReader(raw), &imap.AppendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	var wire trafficCount
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         rigCaps(),
		InsecureAuth: true,
		DebugWriter:  &wire,
	})
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	base := startAlborz(t, ln.Addr().String())
	c := login(t, base)
	message := fmt.Sprintf("%s/message/INBOX/%d", base, appended.UID)
	before := wire.n.Load()
	page := get(t, c, message)
	if moved := wire.n.Load() - before; moved >= 1<<21 {
		t.Errorf("opening the message moved %d bytes, as much as its attachment", moved)
	}
	if !strings.Contains(page, "The signed words.") || !strings.Contains(page, "/signature") {
		t.Fatal("the page shows neither the signed words nor a place for the verdict")
	}
	if verdict := get(t, c, message+"/signature"); !strings.Contains(verdict, "sig-good") {
		t.Errorf("the verdict on a good signature reads %q", verdict)
	}
}

type trafficCount struct{ n atomic.Int64 }

func (w *trafficCount) Write(b []byte) (int, error) {
	w.n.Add(int64(len(b)))
	return len(b), nil
}

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

func sendFrom(t *testing.T, c *http.Client, base, path, page string, fields ...string) {
	t.Helper()
	resp := postCompose(t, c, base, path, page, fields...)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("POST %s: %s", path, resp.Status)
	}
}

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
	if !strings.Contains(noticeBar(form), "User unknown") {
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

// An undo with an undo would loop.
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
	return regexp.MustCompile(`(?s)<dialog open class="notice[^"]*"[^>]*>(.*?)</dialog>`).FindString(body)
}

func alertText(body string) string {
	return regexp.MustCompile(`(?s)<div class="alert"[^>]*>(.*?)</div>`).FindString(body)
}

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

func rowRefs(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`name="refs" value="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

// The second page is where an off-by-one cut or a one-account total shows.
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
		if got := pagerSteps(t, body); !slices.Equal(got, []string{"", "2"}) {
			t.Errorf("%s: the second page steps to pages %q, want the first and the third", list, got)
		}
	}
}

// pagerSteps are the page parameters the pager's links name, the first page's being empty.
func pagerSteps(t *testing.T, body string) []string {
	t.Helper()
	var steps []string
	for _, m := range regexp.MustCompile(`<a href="([^"]+)" class="icon-button"`).FindAllStringSubmatch(between(body, `class="actions-pagination"`, "</div>"), -1) {
		u, err := url.Parse(html.UnescapeString(m[1]))
		if err != nil {
			t.Fatalf("pager link %q: %v", m[1], err)
		}
		steps = append(steps, u.Query().Get("page"))
	}
	return steps
}

// A message opened from the merged inbox walks the merged rows, across
// accounts, reading past the page it was on for the row after its last.
func TestAMergedListIsWalkedInItsOwnOrder(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := bothAccounts(t, base)
	all := rowRefs(get(t, c, base+"/mailbox/INBOX?ipp=100"))
	if len(all) != 6 {
		t.Fatalf("the merged inbox lists %v, want both accounts' six messages", all)
	}
	subject := regexp.MustCompile(`class="message-list-subject[^"]*" role="cell">\s*<a [^>]*href="([^"]+)"`)
	step := func(page, key string) string {
		m := regexp.MustCompile(`<a [^>]*href="([^"]+)"[^>]*data-key="` + key + `"`).FindStringSubmatch(page)
		if m == nil {
			return ""
		}
		u, err := url.Parse(html.UnescapeString(m[1]))
		if err != nil {
			t.Fatalf("neighbour link %q: %v", m[1], err)
		}
		folder, uid, _ := strings.Cut(strings.TrimPrefix(u.Path, "/message/"), "/")
		return u.Query().Get("account") + "|" + folder + "|" + uid
	}
	// The last row of the second page, whose next is on the third, and a row inside one page.
	for _, at := range []struct {
		list     string
		row, pos int
	}{{"/mailbox/INBOX?ipp=2&page=1", 1, 3}, {"/mailbox/INBOX?ipp=100", 4, 4}} {
		links := subject.FindAllStringSubmatch(get(t, c, base+at.list), -1)
		if len(links) <= at.row {
			t.Fatalf("%s lists %d rows", at.list, len(links))
		}
		msg := get(t, c, base+html.UnescapeString(links[at.row][1]))
		if prev, next := step(msg, "k"), step(msg, "j"); prev != all[at.pos-1] || next != all[at.pos+1] {
			t.Errorf("%s: row %d steps to %q and %q, want %q and %q", at.list, at.row, prev, next, all[at.pos-1], all[at.pos+1])
		}
		if !strings.Contains(msg, fmt.Sprintf(">%d of 6<", at.pos+1)) {
			t.Errorf("%s: row %d does not say it is %d of 6", at.list, at.row, at.pos+1)
		}
	}
}

// A folder that is no role is merged from the accounts that hold it; one
// that no account holds is not there, rather than an empty list.
func TestAMergedFolderByNameListsWhoHoldsIt(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := bothAccounts(t, base)
	refs := rowRefs(get(t, c, base+"/mailbox/INBOX%2FLists"))
	if len(refs) != 5 {
		t.Fatalf("the merged INBOX/Lists lists %v, want the five seeded messages", refs)
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref, smokeUser+"|INBOX/Lists|") {
			t.Errorf("row %q is not from the account holding the folder's mail", ref)
		}
	}
	resp, err := c.Get(base + "/mailbox/Nowhere")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a folder no account holds answered %s, want 404", resp.Status)
	}
}

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
		// The loss is noticed a moment after Close returns; until then the cache answers.
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

func rowAccounts(body string) map[string]int {
	out := map[string]int{}
	for _, m := range regexp.MustCompile(`name="refs" value="([^|]+)\|`).FindAllStringSubmatch(body, -1) {
		out[m[1]]++
	}
	return out
}

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

// A notice filed under an account would turn up later on something unrelated.
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

// The session id is shown to the account's other browsers, so it alone must open nothing.
func TestASessionOpensOnlyToItsOwnSecret(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := login(t, base)

	at, _ := url.Parse(base)
	var id string
	for _, ck := range c.Jar.Cookies(at) {
		if ck.Name == "alborz_session" {
			id, _, _ = strings.Cut(ck.Value, ".")
		}
	}
	if id == "" {
		t.Fatal("signing in left no session cookie")
	}
	if uids := messageUIDs(get(t, c, base+"/mailbox/INBOX")); len(uids) == 0 {
		t.Fatal("the session's own browser was not shown its inbox")
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/mailbox/INBOX", nil)
	req.AddCookie(&http.Cookie{Name: "alborz_session", Value: id + ".x"})
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

func perPage(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	m := regexp.MustCompile(`id="messages_per_page"\s+value="(\d+)"`).FindStringSubmatch(get(t, c, base+"/settings"))
	if m == nil {
		t.Fatal("the settings page shows no page size")
	}
	return m[1]
}

func TestProfileReachesEveryAccount(t *testing.T) {
	base := startAlborz(t, startIMAP(t))
	c := bothAccounts(t, base)
	if resp := postForm(t, c, base+"/settings", url.Values{"messages_per_page": {"17"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the profile: %s", resp.Status)
	}
	for _, account := range []string{smokeUser, smokeUser2} {
		if got := perPage(t, loginAs(t, base, account), base); got != "17" {
			t.Errorf("%s holds a page size of %s, want 17", account, got)
		}
	}
}

func TestSigningInAskedAboutAnotherProfile(t *testing.T) {
	for _, answer := range []struct{ path, want string }{
		{"/settings/profile/take", "17"},
		{"/settings/profile/discard", "50"},
	} {
		t.Run(answer.path, func(t *testing.T) {
			base := startAlborz(t, startIMAP(t))
			postForm(t, loginAs(t, base, smokeUser), base+"/settings", url.Values{"messages_per_page": {"17"}})

			c := loginAs(t, base, smokeUser2)
			resp, err := c.PostForm(base+"/login?add=1", url.Values{"username": {smokeUser}, "password": {smokePass}})
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			buf.ReadFrom(resp.Body)
			resp.Body.Close()
			bar := noticeBar(buf.String())
			if !strings.Contains(bar, "Messages per page") || !strings.Contains(bar, "17") || !strings.Contains(bar, "50") {
				t.Fatalf("no notice names the differing page size:\n%s", bar)
			}
			if got := perPage(t, c, base); got != "50" {
				t.Fatalf("the session took %s before the reader answered", got)
			}

			var fields url.Values
			for _, f := range regexp.MustCompile(`(?s)<form method="post" action="([^"]+)" class="notice-action">(.*?)</form>`).FindAllStringSubmatch(bar, -1) {
				if f[1] != answer.path {
					continue
				}
				fields = url.Values{}
				for _, in := range regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(f[2], -1) {
					fields.Add(in[1], html.UnescapeString(in[2]))
				}
			}
			if fields == nil {
				t.Fatalf("the notice offers no %s:\n%s", answer.path, bar)
			}
			if resp := postForm(t, c, base+answer.path, fields); resp.StatusCode != http.StatusFound {
				t.Fatalf("answering: %s", resp.Status)
			}

			if got := perPage(t, c, base); got != answer.want {
				t.Errorf("the session reads %s, want %s", got, answer.want)
			}
			for _, account := range []string{smokeUser, smokeUser2} {
				if got := perPage(t, loginAs(t, base, account), base); got != answer.want {
					t.Errorf("%s holds %s, want %s", account, got, answer.want)
				}
			}
		})
	}
}

func TestCachingOffServesEveryPage(t *testing.T) {
	stub := &davStub{cals: map[string]davCal{davHome + "personal/": {"Personal", "#3366cc"}},
		refuse: map[string]bool{}, keep: map[string]bool{}, gone: map[string]bool{}}
	dav := httptest.NewServer(stub)
	defer dav.Close()
	base := serveAlborz(t, []string{
		"test.local=imap+insecure://" + startIMAP(t),
		"test.local=caldav+insecure://" + strings.TrimPrefix(dav.URL, "http://") + "/dav",
	}, false)
	c := login(t, base)

	if len(messageUIDs(get(t, c, base+"/mailbox/INBOX"))) == 0 {
		t.Fatal("the inbox listed no messages")
	}
	unread := messageUIDs(get(t, c, base+"/mailbox/INBOX?view=unread"))
	if len(unread) == 0 {
		t.Fatal("no unread message to open")
	}
	uid := unread[0]
	if page := get(t, c, base+"/message/INBOX/"+uid+"?part=1"); !strings.Contains(page, "message-flag-form") {
		t.Fatal("the message page did not render")
	}
	if slices.Contains(messageUIDs(get(t, c, base+"/mailbox/INBOX?view=unread")), uid) {
		t.Errorf("message %s is still unread after it was opened", uid)
	}
	if !strings.Contains(get(t, c, base+"/calendar"), "Personal") {
		t.Error("the calendar page does not show the server's calendar")
	}
}

// A part past 2 MiB shows its start, cut on a whole rune or tag, and
// no more than that crosses the wire; the link fetches all of it.
func TestLongPartShowsItsStart(t *testing.T) {
	const shown = 2 << 20
	// 57 bytes to a base64 line, and an odd start: the cut splits an "é".
	text := "PARTSTART\r\n" + strings.Repeat(strings.Repeat("é", 1000)+"\r\n", 1600) + "PARTEND\r\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	var wrapped strings.Builder
	for len(encoded) > 76 {
		wrapped.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	wrapped.WriteString(encoded + "\r\n")
	// One line, and the cut falls inside a tag.
	markup := "<p>PARTSTART</p>" + strings.Repeat("<b>w</b>", (shown-40)/8) + `<a href="https://example.org/">link</a>` +
		strings.Repeat("<i>w</i>", 1<<17) + "<p>PARTEND</p>"

	for name, part := range map[string]struct{ header, body string }{
		"text": {"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n", wrapped.String()},
		"html": {"Content-Type: text/html; charset=utf-8\r\n", markup},
	} {
		t.Run(name, func(t *testing.T) {
			raw := "From: <eve@example.org>\r\nTo: <" + smokeUser + ">\r\nSubject: Long\r\nMIME-Version: 1.0\r\n" +
				part.header + "\r\n" + part.body
			mem := imapmemserver.New()
			user := imapmemserver.NewUser(smokeUser, smokePass)
			if err := user.Create("INBOX", nil); err != nil {
				t.Fatal(err)
			}
			appended, err := user.Append("INBOX", strings.NewReader(raw), &imap.AppendOptions{})
			if err != nil {
				t.Fatal(err)
			}
			mem.AddUser(user)
			var wire trafficCount
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			srv := imapserver.New(&imapserver.Options{
				NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
					return mem.NewSession(), nil, nil
				},
				Caps:         rigCaps(),
				InsecureAuth: true,
				DebugWriter:  &wire,
			})
			go srv.Serve(ln)
			t.Cleanup(func() { srv.Close() })

			base := startAlborz(t, ln.Addr().String())
			c := login(t, base)
			message := fmt.Sprintf("%s/message/INBOX/%d", base, appended.UID)
			before := wire.n.Load()
			page := get(t, c, message+"?part=1")
			if moved := wire.n.Load() - before; moved > shown+64<<10 {
				t.Errorf("opening the message moved %d bytes", moved)
			}
			if !strings.Contains(page, "PARTSTART") || strings.Contains(page, "PARTEND") {
				t.Fatal("the page does not show the part's start, or shows its end")
			}
			if !utf8.ValidString(page) || strings.ContainsRune(page, utf8.RuneError) {
				t.Error("the cut left a broken character on the page")
			}
			if !strings.Contains(page, "The rest of this message part is not shown.") {
				t.Error("the page does not say the part is cut")
			}
			if !slices.Contains(rawPartHrefs(page), fmt.Sprintf("/message/INBOX/%d/raw?part=1", appended.UID)) {
				t.Fatalf("no link to the whole part among %q", rawPartHrefs(page))
			}
			whole := get(t, c, message+"/raw?part=1")
			if !strings.HasSuffix(whole, "PARTEND\r\n") && !strings.HasSuffix(whole, "<p>PARTEND</p>") {
				t.Error("the download is not the whole part")
			}
		})
	}
}

func TestUnbrokenLineShowsAsText(t *testing.T) {
	addr, _ := startIMAPServer(t, rigCaps())
	ic, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ic.Close()
	if err := ic.Login(smokeUser, smokePass).Wait(); err != nil {
		t.Fatal(err)
	}
	line := "LINESTART" + strings.Repeat(" word", 100<<10/5) + " LINEEND"
	raw := "From: <eve@example.org>\r\nTo: <" + smokeUser + ">\r\nSubject: One line\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" + line + "\r\n"
	cmd := ic.Append("INBOX", int64(len(raw)), nil)
	cmd.Write([]byte(raw))
	cmd.Close()
	appended, err := cmd.Wait()
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	base := startAlborz(t, addr)
	c := login(t, base)
	page := get(t, c, fmt.Sprintf("%s/message/INBOX/%d?part=1", base, appended.UID))
	if !strings.Contains(page, "LINESTART") || !strings.Contains(page, "LINEEND") {
		t.Error("a 100 KiB line does not show as text")
	}
}

// asked counts the SORT and THREAD commands a sortingProxy answered.
type asked struct{ sorts, threads atomic.Int32 }

// sortingProxy offers SORT and THREAD for the in-memory server, which has
// neither: it asks SEARCH instead and hands the answer back in its order,
// reversed where the sort was, or two messages to a conversation.
func sortingProxy(t *testing.T, upstream string) (string, *asked) {
	t.Helper()
	counts := &asked{}
	command := regexp.MustCompile(`^(\S+ (?:UID )?)SORT \(([^)]*)\) \S+ (.*)$`)
	threading := regexp.MustCompile(`^(\S+ (?:UID )?)THREAD \S+ \S+ (.*)$`)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", upstream)
			if err != nil {
				client.Close()
				continue
			}
			// The tags the server has yet to answer, in order; the one in front owns an untagged SEARCH.
			var mu sync.Mutex
			type pending struct {
				tag                        string
				sorted, reversed, threaded bool
			}
			var queue []pending
			go func() {
				defer server.Close()
				r := bufio.NewReader(client)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					body := strings.TrimRight(line, "\r\n")
					if m := threading.FindStringSubmatch(body); m != nil {
						counts.threads.Add(1)
						mu.Lock()
						tag, _, _ := strings.Cut(m[1], " ")
						queue = append(queue, pending{tag: tag, threaded: true})
						mu.Unlock()
						line = m[1] + "SEARCH " + m[2] + "\r\n"
					} else if m := command.FindStringSubmatch(body); m != nil {
						counts.sorts.Add(1)
						mu.Lock()
						tag, _, _ := strings.Cut(m[1], " ")
						queue = append(queue, pending{tag: tag, sorted: true, reversed: strings.HasPrefix(m[2], "REVERSE ")})
						mu.Unlock()
						line = m[1] + "SEARCH " + m[3] + "\r\n"
					} else if tag, _, ok := strings.Cut(body, " "); ok && tag != "*" {
						mu.Lock()
						queue = append(queue, pending{tag: tag})
						mu.Unlock()
					}
					if _, err := io.WriteString(server, line); err != nil {
						return
					}
				}
			}()
			go func() {
				defer client.Close()
				r := bufio.NewReader(server)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					body := strings.TrimRight(line, "\r\n")
					line = strings.Replace(line, "CAPABILITY ", "CAPABILITY SORT THREAD=REFERENCES ", 1)
					mu.Lock()
					if nums, ok := strings.CutPrefix(body, "* SEARCH"); ok && len(queue) > 0 && queue[0].threaded {
						line = "* THREAD "
						for pair := range slices.Chunk(strings.Fields(nums), 2) {
							line += "(" + strings.Join(pair, " ") + ")"
						}
						line = strings.TrimRight(line, " ") + "\r\n"
					}
					if nums, ok := strings.CutPrefix(body, "* SEARCH"); ok && len(queue) > 0 && queue[0].sorted {
						uids := strings.Fields(nums)
						if queue[0].reversed {
							slices.Reverse(uids)
						}
						line = strings.TrimRight("* SORT "+strings.Join(uids, " "), " ") + "\r\n"
					}
					if tag, _, ok := strings.Cut(body, " "); ok {
						if i := slices.IndexFunc(queue, func(p pending) bool { return p.tag == tag }); i >= 0 {
							queue = slices.Delete(queue, i, i+1)
						}
					}
					mu.Unlock()
					if _, err := io.WriteString(client, line); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), counts
}

// A conversation list is threaded once for all its pages, and a message
// opened from it walks the conversations' order, not the folder's.
func TestAConversationListThreadsOnceAndItsMessagesWalkIt(t *testing.T) {
	addr, asked := sortingProxy(t, startIMAP(t))
	base := startAlborz(t, addr)
	c := login(t, base)
	list := base + "/mailbox/INBOX?sort=thread&ipp=1"
	var order []string
	var links []string
	for page := range 3 {
		body := get(t, c, fmt.Sprintf("%s&page=%d", list, page))
		order = append(order, rowUIDs(body)...)
		for _, m := range regexp.MustCompile(`class="message-list-subject[^"]*" role="cell">\s*<a [^>]*href="([^"]+)"`).FindAllStringSubmatch(body, -1) {
			links = append(links, m[1])
		}
	}
	if len(order) != 5 || len(links) != 5 || slices.IsSortedFunc(order, func(a, b string) int { return strings.Compare(b, a) }) {
		t.Fatalf("three pages of one conversation each list %v, want the five seeded messages out of the folder's order", order)
	}
	msg := get(t, c, base+html.UnescapeString(links[2]))
	if prev, next := neighbourUID(t, msg, "k"), neighbourUID(t, msg, "j"); prev != order[1] || next != order[3] {
		t.Errorf("the third message steps to %q and %q, want %q and %q", prev, next, order[1], order[3])
	}
	if n := asked.threads.Load(); n != 1 {
		t.Errorf("three pages and a message sent %d THREADs, want 1", n)
	}
}

// neighbourUID is the UID a message page's k or j link leads to.
func neighbourUID(t *testing.T, page, key string) string {
	t.Helper()
	m := regexp.MustCompile(`<a [^>]*href="([^"]+)"[^>]*data-key="` + key + `"`).FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	u, err := url.Parse(html.UnescapeString(m[1]))
	if err != nil {
		t.Fatalf("neighbour link %q: %v", m[1], err)
	}
	return u.Path[strings.LastIndexByte(u.Path, '/')+1:]
}

// rowUIDs are the UIDs of a mail list's rows, in the order shown.
func rowUIDs(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`name="uids" value="(\d+)" form="messages-form"`).FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// The pages of a sorted list share one SORT, which only a change asks again.
func TestASortedListSortsOncePerChange(t *testing.T) {
	addr, asked := sortingProxy(t, startIMAP(t))
	base := startAlborz(t, addr)
	c := login(t, base)
	list := base + "/mailbox/INBOX?sort=subject&dir=asc&ipp=2"
	var seen []string
	for page := range 3 {
		uids := rowUIDs(get(t, c, fmt.Sprintf("%s&page=%d", list, page)))
		if len(uids) == 0 {
			t.Fatalf("page %d of the sorted inbox lists no rows", page)
		}
		seen = append(seen, uids...)
	}
	if len(seen) != 5 || len(slices.Compact(slices.Sorted(slices.Values(seen)))) != 5 {
		t.Fatalf("three pages of two list %v, want the five seeded messages once each", seen)
	}
	if n := asked.sorts.Load(); n != 1 {
		t.Errorf("three pages of one order sent %d SORTs, want 1", n)
	}
	if resp := postForm(t, c, base+"/message/INBOX/move", url.Values{"uids": {seen[0]}, "to": {"Archive"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("move: %s", resp.Status)
	}
	if uids := rowUIDs(get(t, c, list+"&page=1")); slices.Contains(uids, seen[0]) || len(uids) == 0 {
		t.Errorf("the second page after a move lists %v", uids)
	}
	if n := asked.sorts.Load(); n != 2 {
		t.Errorf("a page after a move sent %d SORTs in all, want 2", n)
	}
}

// A message opened from the third page walks to the rows either side of it in
// that order, taken from the order the pages were cut from.
func TestNeighboursOnTheThirdPageOfASortedList(t *testing.T) {
	addr, asked := sortingProxy(t, startIMAP(t))
	base := startAlborz(t, addr)
	c := login(t, base)
	list := base + "/mailbox/INBOX?sort=subject&dir=desc"
	order := rowUIDs(get(t, c, list+"&ipp=100"))
	if len(order) != 5 {
		t.Fatalf("the sorted inbox lists %v, want the five seeded messages", order)
	}
	page := get(t, c, list+"&ipp=1&page=2")
	link := regexp.MustCompile(`class="message-list-subject[^"]*" role="cell">\s*<a [^>]*href="([^"]+)"`).FindStringSubmatch(page)
	if link == nil {
		t.Fatal("the third page has no row to open")
	}
	msg := get(t, c, base+html.UnescapeString(link[1]))
	step := func(key string) string {
		m := regexp.MustCompile(`<a [^>]*href="([^"]+)"[^>]*data-key="` + key + `"`).FindStringSubmatch(msg)
		if m == nil {
			return ""
		}
		u, err := url.Parse(html.UnescapeString(m[1]))
		if err != nil {
			t.Fatalf("neighbour link %q: %v", m[1], err)
		}
		return u.Path[strings.LastIndexByte(u.Path, '/')+1:]
	}
	if prev, next := step("k"), step("j"); prev != order[1] || next != order[3] {
		t.Errorf("the third message steps to %q and %q, want %q and %q", prev, next, order[1], order[3])
	}
	if !strings.Contains(msg, ">3 of 5<") {
		t.Error("the third message does not say it is 3 of 5")
	}
	if n := asked.sorts.Load(); n != 1 {
		t.Errorf("listing and opening sent %d SORTs, want the one", n)
	}
}
