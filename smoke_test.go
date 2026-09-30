package alborz_test

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-mbox"
	"github.com/emersion/go-message"
)

// A page that 500s is invisible to every test that does not open it.
func TestPagesAnswer(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)

	body := c.Page(t, "mail.mailbox", "INBOX")
	if !strings.Contains(body, "message-row") {
		t.Fatalf("inbox has no messages; the rig seeded none and every other check is vacuous")
	}
	uids := MessageUIDs(body)
	if len(uids) < 4 {
		t.Fatalf("expected the four seeded messages, found %d", len(uids))
	}
	// Without the attachment row, the checks below pass without having run.
	if !strings.Contains(body, "mailbox.attachment") && !strings.Contains(body, "message-mark") {
		t.Fatalf("the seeded attachment leaves no mark on its row; that row is unchecked")
	}

	nestedUIDs := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX/Lists"))
	if len(nestedUIDs) == 0 {
		t.Fatal("the nested folder shows no messages; the escaping is unchecked")
	}
	var nestedParts []string
	for _, uid := range nestedUIDs {
		page := c.Page(t, "mail.message", "INBOX/Lists", uid)
		nestedParts = append(nestedParts, a.RawParts(page)...)
	}
	if len(nestedParts) == 0 {
		t.Fatal("no downloadable part in the nested folder; the escaping is unchecked")
	}
	for _, href := range nestedParts {
		t.Run(href, func(t *testing.T) { c.Follow(t, href) })
	}

	pages := [][]any{
		{"mail.mailbox", "INBOX"}, {"mail.mailbox", "INBOX", url.Values{"view": {"starred"}}},
		{"mail.mailbox", "INBOX", url.Values{"query": {"redesign"}}}, {"mail.mailbox", "INBOX/Lists"},
		{"mail.mailbox", "Drafts"}, {"mail.mailbox", "Junk"}, {"mail.mailbox", "Trash"},
		{"mail.compose"}, {"mail.mailbox.create"}, {"settings.profile"}, {"mail.account"},
		{"settings.servers"}, {"mail.signatures"}, {"mail.signatures.create"},
	}
	for _, uid := range uids {
		pages = append(pages,
			[]any{"mail.message", "INBOX", uid},
			[]any{"mail.message.reply", "INBOX", uid},
			[]any{"mail.message.forward", "INBOX", uid},
			[]any{"mail.messages.forward", "INBOX", url.Values{"uids": {uid}}},
		)
	}
	for _, p := range pages {
		path := a.Path(p[0].(string), p[1:]...)
		t.Run(path, func(t *testing.T) { c.Follow(t, path) })
	}
}

// A sign-in goes on to the page that asked for it, and never off the site.
func TestSignInStaysOnTheSite(t *testing.T) {
	a := Alborz(t)
	inbox := a.Path("mail.mailbox", "INBOX")
	for next, want := range map[string]string{
		a.Path("settings.profile"): a.Path("settings.profile"),
		"//evil.example/":          inbox,
		"https://evil.example/":    inbox,
		`/\evil`:                   inbox,
		a.Path("auth.login"):       inbox,
	} {
		resp := a.Client().Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}, "next": {next}})
		if got := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || got != want {
			t.Errorf("next %q answered %s to %q, want %q", next, resp.Status, got, want)
		}
	}
}

// The login must still work after answering the bad query.
func TestUnknownSearchKeySurvives(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	if body := c.Page(t, "mail.mailbox", "INBOX", url.Values{"query": {"foo:bar"}}); body == "" {
		t.Fatal("the unrecognised key produced no page")
	}
	if body := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(body, "message-row") {
		t.Fatal("the login stopped serving mail after an unrecognised search key")
	}
}

// The prefetch runs behind the listing, so the sequence repeats to meet a held body.
func TestOpeningAHeldMessageAfterAFlagChange(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	for range 3 {
		uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
		if len(uids) == 0 {
			t.Fatal("no messages listed")
		}
		time.Sleep(150 * time.Millisecond)
		next := a.Path("mail.message", "INBOX", uids[0], url.Values{"part": {"1"}})
		c.Post(t, "mail.messages.flag", url.Values{
			"uids": {uids[0]}, "flags": {`\Flagged`}, "action": {"add"}, "next": {next}}, "INBOX")
		if !strings.Contains(c.Follow(t, next), "message-flag-form") {
			t.Fatal("the message page did not render after the flag change")
		}
	}
	if body := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(body, "message-row") {
		t.Fatal("the login stopped serving mail after the flag change")
	}
}

func TestCachedMessageAndSidebarWithoutUpstream(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	path := a.Path("mail.message", "INBOX", uids[0], url.Values{"part": {"1"}})
	read := func(path, target string) (string, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, a.URL+path, nil)
		req.Header.Set("HX-Request", "true")
		req.Header.Set("HX-Target", target)
		resp := c.Do(t, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("response %d", resp.StatusCode)
		}
		return resp.Text, resp.Header.Get("Server-Timing")
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
	a.IMAP.Close()
	c.HTTP.Timeout = time.Second
	body, timing := read(path, "body")
	if !strings.Contains(body, "message-flag-form") || !strings.Contains(timing, "body_hit") || strings.Contains(timing, "imap_work") {
		t.Fatalf("cached message used upstream: %s", timing)
	}
	body, _ = read(a.Path("mail.sidebar", url.Values{"path": {a.Path("mail.message", "INBOX", uids[0])}}), "aside")
	if !strings.Contains(body, "data-mail-sidebar") || strings.Contains(body, "message-flag-form") {
		t.Fatal("sidebar response rendered the message")
	}
}

func TestStarStickToTheScopedAccount(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)

	scoped := url.Values{"account": {User2}}
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", scoped))
	if len(uids) == 0 {
		t.Fatal("the second account's inbox listed no message")
	}
	uid := uids[0]
	here := a.Path("mail.message", "INBOX", uid, url.Values{"part": {"1"}, "account": {User2}})

	if flagButtonColor(t, c.Follow(t, here)) != "" {
		t.Fatal("the message was already flagged before the test starred it")
	}
	c.Post(t, "mail.messages.flag", url.Values{"uids": {uid}, "color": {"red"}, "next": {here}}, "INBOX", scoped)

	if got := flagButtonColor(t, c.Follow(t, here)); got != "red" {
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
	for _, cls := range strings.Fields(page[start : start+end]) {
		if strings.HasPrefix(cls, "flag-") && cls != "flag-button" {
			return strings.TrimPrefix(cls, "flag-")
		}
	}
	return ""
}

// Redirecting to whatever a mailto parameter names would forward the reader anywhere.
func TestMailtoHandlerRefusesForeignURI(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)

	for _, uri := range []string{"https://evil.example/x", "//evil.example/x", "javascript:alert(1)"} {
		resp := c.Get(t, "mail.compose", url.Values{"mailto": {uri}})
		if loc := resp.Header.Get("Location"); loc != "" {
			t.Fatalf("mailto=%s redirected to %q", uri, loc)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("mailto=%s answered %s, want 400", uri, resp.Status)
		}
	}

	// Otherwise the check above passes by refusing everything.
	loc := c.Get(t, "mail.compose", url.Values{"mailto": {"mailto:a@b.example?subject=Hi"}}).Header.Get("Location")
	if !strings.HasPrefix(loc, a.Path("mail.compose")+"?") || !strings.Contains(loc, "to=a@b.example") ||
		!strings.Contains(loc, "subject=Hi") {
		t.Fatalf("a real mailto URI redirected to %q", loc)
	}
}

func TestExportFailsAsAnError(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)

	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) < 2 {
		t.Fatalf("need two seeded messages to export, found %d", len(uids))
	}

	resp := c.Post(t, "mail.messages.export", url.Values{"uids": {"999999"}}, "INBOX")
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
	resp = c.Post(t, "mail.messages.export", url.Values{"uids": uids[:2]}, "INBOX")
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/mbox") {
		t.Fatalf("a good export was served as %q", ct)
	}
	if n := strings.Count(resp.Text, "\nFrom ") + strings.Count(resp.Text[:min(6, len(resp.Text))], "From "); n < 2 {
		t.Errorf("expected two messages, found %d separators", n)
	}
	if strings.Contains(resp.Text, "<!DOCTYPE") {
		t.Error("the export carries an HTML page inside it")
	}

	// A field per message is megabytes on a large folder.
	page := c.Post(t, "mail.messages.export", url.Values{"everything": {"1"}, "next": {a.Path("mail.mailbox", "INBOX")}, "ask": {"1"}}, "INBOX").Text
	if !strings.Contains(page, `name="everything"`) || strings.Contains(page, `name="uids"`) {
		t.Errorf("the export page of a whole list carries its messages, or not the list")
	}
}

// wrapped is b in base64, in lines of 76.
func wrapped(b []byte) string {
	encoded := base64.StdEncoding.EncodeToString(b)
	var w strings.Builder
	for len(encoded) > 76 {
		w.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	w.WriteString(encoded + "\r\n")
	return w.String()
}

// An export is the reader's copy of their mail: every message whole, in order.
func TestExportHoldsEveryMessageWhole(t *testing.T) {
	a := Alborz(t)
	file := make([]byte, 3<<20)
	for i := range file {
		file[i] = byte(i * 7)
	}
	var sent []string
	for i := range 3 {
		raw := fmt.Sprintf("From: <eve@example.org>\r\nTo: <%s>\r\nSubject: Export %d\r\nMessage-ID: <export-%d@test>\r\n"+
			"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b\"\r\n\r\n"+
			"--b\r\nContent-Type: text/plain\r\n\r\nFrom the start of a line.\r\n>From quoted.\r\n"+
			"--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n"+
			"Content-Disposition: attachment; filename=\"file%d.bin\"\r\n\r\n%s--b--\r\n", User, i, i, i, wrapped(file))
		a.IMAP.Append(t, "INBOX", raw)
		sent = append(sent, raw)
	}

	c := a.SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"query": {"subject:Export"}}))
	if len(uids) != len(sent) {
		t.Fatalf("the search finds %d of the %d messages", len(uids), len(sent))
	}
	resp := c.Post(t, "mail.messages.export", url.Values{"uids": uids}, "INBOX")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export: %s", resp.Status)
	}
	archive := mbox.NewReader(strings.NewReader(resp.Text))
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

// A download cut off by the server must fail in the browser, not arrive
// as a shorter file that looks whole.
func TestBrokenDownloadIsNotComplete(t *testing.T) {
	file := make([]byte, 3<<20)
	for i := range file {
		file[i] = byte(i * 7)
	}
	raw := "From: <eve@example.org>\r\nTo: <" + User + ">\r\nSubject: Cut off\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nSee the file.\r\n" +
		"--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"file.bin\"\r\n\r\n" + wrapped(file) + "--b--\r\n"

	for name, download := range map[string]func(a *App, uid imap.UID) *http.Request{
		"mbox": func(a *App, uid imap.UID) *http.Request {
			req, _ := http.NewRequest(http.MethodPost, a.URL+a.Path("mail.messages.export", "INBOX"),
				strings.NewReader(url.Values{"uids": {fmt.Sprint(uid)}}.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return req
		},
		"zip": func(a *App, uid imap.UID) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, a.URL+a.Path("mail.message.attachments", "INBOX", uid), nil)
			return req
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := Alborz(t, WithIMAP(IMAP(t, Cut(1<<20))))
			uid := a.IMAP.Append(t, "INBOX", raw)
			c := a.SignIn(t, User)
			a.IMAP.Arm()
			// Held back by compression, the cut may come before the response does.
			resp, err := c.HTTP.Do(download(a, uid))
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

// It asserts the bytes sent to the SMTP server, the only place the answer is visible.
func TestSendHTMLFollowsWhatWasWritten(t *testing.T) {
	smtp := SMTP(t)
	a := Alborz(t, WithSMTP(smtp))
	c := a.SignIn(t, User)

	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	var listUID string
	for _, uid := range uids {
		if strings.Contains(c.Page(t, "mail.message", "INBOX", uid, url.Values{"part": {"1"}}), "Mailing list") {
			listUID = uid
			break
		}
	}
	if listUID == "" {
		t.Fatal("the seeded list message is missing; the list case would go unchecked")
	}

	compose := func() string { return c.Page(t, "mail.compose") }

	c.Send(t, compose(), url.Values{"text": {"Hello."}, "html": {"<p>Hello.</p>"}}, "mail.compose")
	if !strings.Contains(smtp.Last(), "text/html") {
		t.Errorf("a message written in the editor went out without its HTML:\n%s", headOf(smtp.Last()))
	}

	c.Send(t, compose(), url.Values{"text": {"Hello.\n\nEnglish only."}}, "mail.compose")
	if strings.Contains(smtp.Last(), "multipart/alternative") {
		t.Errorf("left-to-right plain text was sent with an HTML part:\n%s", headOf(smtp.Last()))
	}

	c.Send(t, compose(), nil, "mail.compose")
	if !strings.Contains(smtp.Last(), "multipart/alternative") {
		t.Errorf("right-to-left plain text was sent with no direction:\n%s", headOf(smtp.Last()))
	}

	reply := []any{"INBOX", listUID, url.Values{"part": {"1"}}}
	c.Send(t, c.Page(t, "mail.message.reply", reply...), nil, "mail.message.reply", reply...)
	if strings.Contains(smtp.Last(), "multipart/alternative") {
		t.Errorf("a reply to list mail carried an HTML part:\n%s", headOf(smtp.Last()))
	}

	// An unticked box is absent from the form.
	const ticked = `name="direction_part" id="direction_part" value="1" checked`
	if !strings.Contains(c.Page(t, "settings.profile"), ticked) {
		t.Fatal("the direction part is not on by default")
	}
	if resp := c.Post(t, "settings.profile", url.Values{"reply_position": {"above"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving settings: %s", resp.Status)
	}
	if strings.Contains(c.Page(t, "settings.profile"), ticked) {
		t.Fatal("the settings page still shows the direction part on")
	}
	c.Send(t, compose(), url.Values{"subject": {"turned off"}}, "mail.compose")
	if !strings.Contains(smtp.Last(), "Subject: turned off") {
		t.Fatalf("the message sent with the part turned off never arrived:\n%s", headOf(smtp.Last()))
	}
	if strings.Contains(smtp.Last(), "text/html") {
		t.Errorf("right-to-left plain text carried an HTML part the writer turned off:\n%s", headOf(smtp.Last()))
	}
}

// The draft is the URL's account's; From only says who sends.
func TestSendingAsAnotherAccountKeepsItsDraft(t *testing.T) {
	smtp := SMTP(t)
	a := Alborz(t, WithSMTP(smtp))
	c := a.SignIn(t, User)
	c.SignIn(t, User2)

	first := url.Values{"account": {User}}
	second := url.Values{"account": {User2}}
	c.Send(t, c.Page(t, "mail.compose", first), url.Values{"save_as_draft": {"1"}}, "mail.compose", first)
	drafts := MessageUIDs(c.Page(t, "mail.mailbox", "Drafts", first))
	if len(drafts) != 1 {
		t.Fatalf("the draft was not saved on the first account; found %d", len(drafts))
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Drafts", second))); n != 1 {
		t.Fatalf("the second account should start with one draft, found %d", n)
	}

	edit := []any{"Drafts", drafts[0], url.Values{"part": {"1"}, "account": {User}}}
	c.Send(t, c.Page(t, "mail.message.edit", edit...), url.Values{"from_account": {User2}}, "mail.message.edit", edit...)

	if !strings.Contains(smtp.Last(), "From: <"+User2+">") {
		t.Errorf("the message did not leave as the chosen account:\n%s", headOf(smtp.Last()))
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Drafts", first))); n != 0 {
		t.Errorf("the sent draft is still on the first account")
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Drafts", second))); n != 1 {
		t.Errorf("the second account's own draft is gone")
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Sent", second))); n != 1 {
		t.Errorf("the sent copy is not on the sending account; found %d", n)
	}
}

func TestReopenedDraftKeepsItsLines(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	text := strings.Repeat("A paragraph long enough to be wrapped when saved. ", 4) + "End."
	c.Send(t, c.Page(t, "mail.compose"), url.Values{"save_as_draft": {"1"}, "text": {text}}, "mail.compose")
	drafts := MessageUIDs(c.Page(t, "mail.mailbox", "Drafts"))
	if len(drafts) != 1 {
		t.Fatalf("want one draft, found %d", len(drafts))
	}
	page := c.Page(t, "mail.message.edit", "Drafts", drafts[0], url.Values{"part": {"1"}})
	if got := html.UnescapeString(Between(page, `data-caret="end">`, "</textarea>")); got != text {
		t.Errorf("the reopened draft reads\n%q\nwant\n%q", got, text)
	}
}

// Another client's deleted message is its to expunge, never ours.
func TestSavingADraftLeavesOthersDeletedMessages(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []IMAPOption
		// Without UIDPLUS only a plain EXPUNGE could remove the replaced draft, and that takes the others too.
		replacedLeft bool
	}{
		{"uidplus", nil, false},
		{"without uidplus", []IMAPOption{Without(imap.CapIMAP4rev2)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Alborz(t, WithIMAP(IMAP(t, tc.opts...)))
			c := a.SignIn(t, User)
			ic := a.IMAP.Dial(t)
			a.IMAP.Append(t, "Drafts", "From: <eve@example.org>\r\nSubject: discarded elsewhere\r\n\r\nGone later.\r\n", imap.FlagDeleted)
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

			c.Send(t, c.Page(t, "mail.compose"), url.Values{"save_as_draft": {"1"}}, "mail.compose")
			first := drafts(ours)
			if len(first) != 1 {
				t.Fatalf("want the saved draft, found %d", len(first))
			}
			edit := []any{"Drafts", first[0], url.Values{"part": {"1"}}}
			c.Send(t, c.Page(t, "mail.message.edit", edit...), url.Values{"save_as_draft": {"1"}}, "mail.message.edit", edit...)

			if n := len(drafts(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: "discarded elsewhere"}}})); n != 1 {
				t.Errorf("another client's deleted message was expunged")
			}
			if n := len(drafts(&imap.SearchCriteria{Header: ours.Header, NotFlag: []imap.Flag{imap.FlagDeleted}})); n != 1 {
				t.Errorf("want one live draft after saving again, found %d", n)
			}
			replaced := drafts(&imap.SearchCriteria{UID: []imap.UIDSet{imap.UIDSetNum(first[0])}, Flag: []imap.Flag{imap.FlagDeleted}})
			if left := len(replaced) == 1; left != tc.replacedLeft {
				t.Errorf("replaced draft left marked deleted: %v, want %v", left, tc.replacedLeft)
			}
		})
	}
}

// Both read the message only to decide; opening it is what marks it read.
func TestUnsubscribeAndAddToCalendarLeaveTheMessageUnread(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	ic := a.IMAP.Dial(t)
	a.IMAP.Append(t, "INBOX", "From: <eve@example.org>\r\nTo: <"+User+">\r\nSubject: Planning\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/calendar; charset=utf-8; method=REQUEST\r\n\r\n"+
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//rig//EN\r\nMETHOD:REQUEST\r\n"+
		"BEGIN:VEVENT\r\nUID:planning@rig\r\nDTSTAMP:20260901T090000Z\r\nDTSTART:20261001T090000Z\r\n"+
		"SUMMARY:Planning\r\nORGANIZER:mailto:eve@example.org\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
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

	for _, action := range []struct {
		subject, route string
		args           func(imap.UID) []any
	}{
		{"Planning", "calendar.frommessage", func(uid imap.UID) []any {
			return []any{url.Values{"mbox": {"INBOX"}, "uid": {fmt.Sprint(uid)}}}
		}},
		{"Ideas for the redesign", "mail.message.unsubscribe", func(uid imap.UID) []any { return []any{"INBOX", uid} }},
	} {
		uid := uidOf(action.subject)
		c.Post(t, action.route, nil, action.args(uid)...)
		msgs, err := ic.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{Flags: true}).Collect()
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 {
			t.Fatalf("%s: want the message back, got %d", action.route, len(msgs))
		}
		if slices.Contains(msgs[0].Flags, imap.FlagSeen) {
			t.Errorf("%s marked the message read", action.route)
		}
	}
}

func TestBadMessageReferenceIsTheCallersFault(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	for _, route := range []string{"mail.message", "mail.message.raw", "mail.message.eml"} {
		if resp := c.Get(t, route, "INBOX", "notanumber"); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s: %s", route, resp.Status)
		}
	}
}

func TestSendWithoutMessageIDStillSends(t *testing.T) {
	smtp := SMTP(t)
	c := Alborz(t, WithSMTP(smtp)).SignIn(t, User)
	resp := c.Upload(t, "mail.compose", url.Values{"to": {"friend@example.org"}, "subject": {"no id"}, "text": {"Body."}}, nil)
	if resp.StatusCode >= 400 {
		t.Fatalf("composing without message_id: %s", resp.Status)
	}
	if !strings.Contains(smtp.Last(), "Message-Id: <") {
		t.Errorf("the message left without an id:\n%s", headOf(smtp.Last()))
	}
}

func TestRefusedSendKeepsItsUploads(t *testing.T) {
	smtp := SMTP(t)
	c := Alborz(t, WithSMTP(smtp)).SignIn(t, User)

	resp := c.Upload(t, "mail.attachment.add", nil, []File{{Field: "attachments", Name: "kept.txt", Body: strings.NewReader("the attached words")}})
	var uploads []struct{ ID, Src string }
	if err := json.Unmarshal([]byte(resp.Text), &uploads); err != nil || len(uploads) != 1 {
		t.Fatalf("upload: %s, %v, %v", resp.Status, uploads, err)
	}

	resp = c.Compose(t, c.Page(t, "mail.compose"),
		url.Values{"to": {"refused@example.org"}, "subject": {"kept"}, "attachment-uuids": {uploads[0].ID}}, "mail.compose")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a refused recipient was answered with %s", resp.Status)
	}
	kept := Between(resp.Text, `data-uuid="`, `"`)
	if kept != uploads[0].ID {
		t.Fatalf("the refused form holds upload %q, want %q", kept, uploads[0].ID)
	}

	c.Send(t, resp.Text, url.Values{"subject": {"kept"}, "attachment-uuids": {kept}}, "mail.compose")
	got := smtp.Last()
	if !strings.Contains(got, "Subject: kept") {
		t.Fatalf("the retry was not sent:\n%s", headOf(got))
	}
	if !strings.Contains(got, `filename=kept.txt`) && !strings.Contains(got, `filename="kept.txt"`) {
		t.Errorf("the retry left without its attachment:\n%s", headOf(got))
	}
}

func TestPastedImageTravelsBesideTheHTML(t *testing.T) {
	smtp := SMTP(t)
	c := Alborz(t, WithSMTP(smtp)).SignIn(t, User)

	resp := c.Upload(t, "mail.attachment.add", nil, []File{{Field: "attachments", Name: "shot.png", Type: "image/png",
		Body: strings.NewReader("\x89PNG\r\n\x1a\nnot really")}})
	var uploads []struct{ ID, Src string }
	if err := json.Unmarshal([]byte(resp.Text), &uploads); err != nil || len(uploads) != 1 {
		t.Fatalf("upload: %s, %v, %v", resp.Status, uploads, err)
	}

	c.Send(t, c.Page(t, "mail.compose"), url.Values{"subject": {"pasted"},
		"html": {`<p>Look: <img src="` + uploads[0].Src + `" alt="shot.png"></p>`}}, "mail.compose")
	got := smtp.Last()
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

func TestEverySenderShapeOfAFileIsListed(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)

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
			"related", nil, "Body text.", "1", []string{`class="shown-image" src="` + a.Path("mail.message.raw", "Shapes", 11, url.Values{"part": {"2"}}) + `"`}, nil},
	}

	if err := a.IMAP.Dial(t).Create("Shapes", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	for _, s := range shapes {
		var b strings.Builder
		fmt.Fprintf(&b, "From: <eve@example.org>\r\nTo: <%s>\r\nSubject: %s\r\nMIME-Version: 1.0\r\n"+
			"Content-Type: multipart/%s; boundary=\"outer\"\r\n\r\n", User, s.name, s.top)
		for _, p := range s.parts {
			b.WriteString("--outer\r\n" + p)
		}
		b.WriteString("--outer--\r\n")
		a.IMAP.Append(t, "Shapes", b.String())
	}

	listed := regexp.MustCompile(`>\s*([^<>]+?)\s*</a> \([a-z]+/[^,]+, `)
	for i, s := range shapes {
		uid := i + 1
		page := c.Page(t, "mail.message", "Shapes", uid)
		if !strings.Contains(page, html.EscapeString(s.name)) {
			t.Fatalf("%s: message %d is not the message appended", s.name, uid)
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
		raw := c.Get(t, "mail.message.attachments", "Shapes", uid).Text
		archive, err := zip.NewReader(strings.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatalf("%s: attachments.zip: %v", s.name, err)
		}
		if len(archive.File) != len(s.files) {
			t.Errorf("%s: the zip holds %d files and the page lists %d", s.name, len(archive.File), len(s.files))
		}
		if s.open == "" {
			continue
		}
		opened := html.UnescapeString(c.Page(t, "mail.message", "Shapes", uid, url.Values{"part": {s.open}}))
		for _, want := range s.shows {
			if !strings.Contains(opened, want) {
				t.Errorf("%s: part %s does not show %q", s.name, s.open, want)
			}
		}
		for _, unwanted := range s.hides {
			if strings.Contains(opened, unwanted) {
				t.Errorf("%s: part %s shows %q", s.name, s.open, unwanted)
			}
		}
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

func TestNewestFirstWithoutSort(t *testing.T) {
	c := Alborz(t, WithIMAP(IMAP(t, Without(imap.CapSort)))).SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"query": {"example"}}))
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
	a := Alborz(t)
	c := a.SignIn(t, User)
	dates := map[string]string{
		"Mon, 1 Jan 4000 10:00:00 +0000": "January 1, 4000",
		"Fri, 1 Jan 0560 10:00:00 +0000": "January 1, 560",
	}
	var uids []imap.UID
	for date := range dates {
		uids = append(uids, a.IMAP.Append(t, "INBOX", "From: a@example.org\r\nTo: "+User+
			"\r\nSubject: Dated\r\nDate: "+date+"\r\n\r\n"))
	}
	for _, reading := range []url.Values{
		{"calendar": {"shcal"}, "secondary": {""}},
		{"calendar": {"gregorian"}, "secondary": {"shcal"}},
	} {
		c.Post(t, "settings.profile", reading)
		list := c.Page(t, "mail.mailbox", "INBOX")
		for _, uid := range uids {
			c.Page(t, "mail.message", "INBOX", uid)
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
	a := Alborz(t)
	c := a.SignIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")
	a.Put(t, coll+"far.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:far\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:40000101T100000Z\r\nDTEND:40000101T110000Z\r\n"+
		"SUMMARY:Far\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	far := Here + coll + "far.ics"
	saved := func() string { return a.DAV(t, http.MethodGet, coll+"far.ics", nil, "").Text }

	c.Post(t, "settings.profile", url.Values{"calendar": {"shcal"}, "secondary": {""}})
	for _, month := range []string{"3178-01", "-62-01"} {
		if resp := c.Get(t, "calendar.month", url.Values{"month": {month}}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("month %s answered %s, want it refused", month, resp.Status)
		}
	}
	typed := url.Values{"summary": {"Typed"}, "calendar": {User + "|" + Here + coll},
		"start": {"3178/01/01 10:00"}, "end": {"3178/01/01 11:00"}, "start-calendar": {"shcal"}, "end-calendar": {"shcal"}}
	if resp := c.Post(t, "calendar.event.create", typed); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a typed year past the reckoning answered %s, want the form refused", resp.Status)
	}

	page := c.Page(t, "calendar.event.update", far)
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
	if resp := c.Post(t, "calendar.event.update", shown, far); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the form as shown answered %s", resp.Status)
	}
	if body := saved(); !strings.Contains(body, "DTSTART:40000101T100000Z") || !strings.Contains(body, "Far again") {
		t.Errorf("the saved event moved: %s", body)
	}

	early := url.Values{"summary": {"Early"}, "allday": {"on"},
		"start_date": {"0100-01-01"}, "end_date": {"0100-01-01"}, "start_date-calendar": {"shcal"}, "end_date-calendar": {"shcal"}}
	if resp := c.Post(t, "calendar.event.update", early, far); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving Solar Hijri 0100-01-01 answered %s", resp.Status)
	}
	// 1 Farvardin 100 falls in March 721.
	if body := saved(); !strings.Contains(body, "DTSTART;VALUE=DATE:0721032") {
		t.Errorf("Solar Hijri 0100-01-01 was not saved as a day of Nowruz 721: %s", body)
	}

	c.Post(t, "settings.profile", url.Values{"calendar": {"gregorian"}, "secondary": {"shcal"}})
	c.Page(t, "calendar.month", url.Values{"month": {"4000-01"}})
}

func TestComposeOpensFromTheAccountNamed(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	c.SignIn(t, User2)
	for _, account := range []string{User, User2} {
		if page := c.Page(t, "mail.compose", url.Values{"account": {account}}); !strings.Contains(page, `value="`+account+`" selected`) {
			t.Errorf("compose for %s does not select it in From", account)
		}
	}
	if page := c.Page(t, "mail.compose"); !strings.Contains(page, `value="`+User+`" selected`) {
		t.Errorf("compose without an account does not select the first listed one")
	}
}

func TestMessagesMoveDeleteAndFlag(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) < 3 {
		t.Fatalf("need three seeded messages, found %d", len(uids))
	}

	if resp := c.Post(t, "mail.messages.move", url.Values{"uids": {uids[0]}, "to": {"Archive"}}, "INBOX"); resp.StatusCode != http.StatusFound {
		t.Fatalf("move: %s", resp.Status)
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Archive"))); n != 1 {
		t.Errorf("Archive holds %d messages after a move, want 1", n)
	}

	if resp := c.Post(t, "mail.messages.delete", url.Values{"uids": {uids[1]}}, "INBOX"); resp.StatusCode != http.StatusFound {
		t.Fatalf("delete: %s", resp.Status)
	}
	left := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(left) != len(uids)-2 || slices.Contains(left, uids[1]) {
		t.Errorf("INBOX after a move and a delete: %v", left)
	}

	if resp := c.Post(t, "mail.messages.flag", url.Values{"uids": {uids[2]}, "flags": {"\\Flagged"}, "action": {"add"}}, "INBOX"); resp.StatusCode != http.StatusFound {
		t.Fatalf("flag: %s", resp.Status)
	}
	if starred := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"view": {"starred"}})); len(starred) != 1 || starred[0] != uids[2] {
		t.Errorf("starred view after flagging %s: %v", uids[2], starred)
	}
}

func TestStarViewKeepsItsQuery(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	refs := regexp.MustCompile(`name="refs" value="([^"]+)"`)
	var starred []string
	for _, query := range []string{"from:eve", "from:hal"} {
		uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"query": {query}}))
		if len(uids) != 1 {
			t.Fatalf("%q answers %d messages in INBOX, want 1", query, len(uids))
		}
		starred = append(starred, uids[0])
	}
	c.Post(t, "mail.messages.flag", url.Values{"uids": starred, "flags": {"\\Flagged"}, "action": {"add"}}, "INBOX")

	narrowed := a.Path("mail.search", url.Values{"view": {"starred"}, "query": {"from:eve"}})
	if rows := refs.FindAllString(c.Follow(t, narrowed), -1); len(rows) != 1 {
		t.Fatalf("starred and from:eve lists %d rows, want the one that is both", len(rows))
	}
	resp := c.Post(t, "mail.role.act", url.Values{"everything": {"1"}, "next": {narrowed}}, "INBOX", url.Values{"action": {"move"}, "to": {"Archive"}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("move: %s", resp.Status)
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Archive"))); n != 1 {
		t.Errorf("Archive holds %d after moving everything starred from eve, want 1", n)
	}
}

func TestFoldersAreMadeAndRemoved(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	if resp := c.Post(t, "mail.mailbox.create", url.Values{"name": {"Projects"}, "location": {"0"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("new folder: %s", resp.Status)
	}
	if page := c.Page(t, "mail.mailbox", "Projects"); !strings.Contains(page, "Projects") {
		t.Fatalf("the new folder is not listed")
	}
	if resp := c.Post(t, "mail.mailbox.delete", nil, "Projects"); resp.StatusCode != http.StatusFound {
		t.Fatalf("delete folder: %s", resp.Status)
	}
	if resp := c.Get(t, "mail.mailbox", "Projects"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a deleted folder still answers %s", resp.Status)
	}
}

func TestAccountsScopeAndSignOut(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)

	merged := c.Page(t, "mail.mailbox", "INBOX")
	for _, account := range []string{User, User2} {
		if !strings.Contains(merged, `href="`+a.Path("mail.mailbox", "INBOX", url.Values{"account": {account}})+`"`) {
			t.Errorf("the merged view's rail does not offer %s", account)
		}
	}
	if !strings.Contains(merged, `class="acct-folders aggregate-folders"`) {
		t.Errorf("the merged view has no merged entry in the rail")
	}
	scoped := c.Page(t, "mail.mailbox", "INBOX", url.Values{"account": {User2}})
	if !strings.Contains(scoped, `popovertarget="menu-accounts"><span class="label"><bdi class="ltr">`+User2) {
		t.Errorf("a page scoped to %s does not say so in the nav", User2)
	}
	if !strings.Contains(scoped, `class="acct-folders aggregate-folders"`) {
		t.Errorf("the scoped view lost the merged entry from the rail")
	}

	for _, account := range []string{User, User2} {
		if resp := c.Post(t, "auth.logout", url.Values{"account": {account}}); resp.StatusCode != http.StatusFound {
			t.Fatalf("logout %s: %s", account, resp.Status)
		}
	}
	resp := c.Post(t, "mail.mailbox", nil, "INBOX")
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), a.Path("auth.login")) {
		t.Errorf("after signing out of every account a page still answered %s to %s", resp.Status, resp.Header.Get("Location"))
	}
}

func TestZipHoldsTheAttachmentsTheCardCounts(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"query": {"from:bob"}}))
	if len(uids) != 1 {
		t.Fatalf("from:bob answers %d messages, want the one with attachments", len(uids))
	}
	parts := regexp.MustCompile(a.Pattern("mail.message.raw", `[^"/]*`, `[^"/]*`) + `\?part=`)
	listed := len(parts.FindAllString(c.Page(t, "mail.message", "INBOX", uids[0]), -1))
	if listed == 0 {
		t.Fatal("the message page lists no attachment")
	}
	raw := c.Get(t, "mail.message.attachments", "INBOX", uids[0]).Text
	archive, err := zip.NewReader(strings.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("attachments.zip: %v", err)
	}
	if len(archive.File) != listed {
		t.Errorf("the zip holds %d files and the page lists %d", len(archive.File), listed)
	}
}

// alborz decodes a part on the way out where the server does not speak BINARY.
func TestDownloadedPartIsTheFileSent(t *testing.T) {
	file := bytes.Repeat([]byte("alborz\x00\xff\r\n"), 1<<16)
	raw := "From: <eve@example.org>\r\nTo: <" + User + ">\r\nSubject: A file\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nSee the file.\r\n" +
		"--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"file.bin\"\r\n\r\n" + wrapped(file) + "--b--\r\n"

	for _, tc := range []struct {
		name string
		opts []IMAPOption
	}{
		{"IMAP4rev2", nil},
		{"IMAP4rev1", []IMAPOption{Caps(imap.CapIMAP4rev1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Alborz(t, WithIMAP(IMAP(t, tc.opts...)))
			uid := a.IMAP.Append(t, "INBOX", raw)
			resp := a.SignIn(t, User).Get(t, "mail.message.raw", "INBOX", uid, url.Values{"part": {"2"}})
			if resp.StatusCode != http.StatusOK || resp.Text != string(file) {
				t.Fatalf("%s: got %d bytes, want the %d sent", resp.Status, len(resp.Text), len(file))
			}
		})
	}
}

// Writing the quoted date must not take the notice the page came to show.
func TestAReplyOrForwardShowsThePendingNotice(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) == 0 {
		t.Fatal("the rig seeded no message to reply to")
	}
	for _, route := range []string{"mail.message.reply", "mail.message.forward"} {
		if resp := c.Post(t, "settings.profile", url.Values{"messages_per_page": {"50"}}); resp.StatusCode != http.StatusFound {
			t.Fatalf("saving the settings: %s", resp.Status)
		}
		if body := c.Page(t, route, "INBOX", uids[0]); !strings.Contains(body, "Settings saved.") {
			t.Errorf("the %s page lost the notice", route)
		}
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
	h.Set("To", "<"+User+">")
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

	smtp := SMTP(t)
	a := Alborz(t, WithSMTP(smtp))
	uid := a.IMAP.Append(t, "INBOX", original.String())
	c := a.SignIn(t, User)
	forward := []any{"INBOX", uid, url.Values{"part": {"1"}}}
	page := c.Page(t, "mail.message.forward", forward...)
	if !strings.Contains(page, `name="prev_attachments" value="2"`) {
		t.Fatal("the forward does not offer the original's file")
	}
	c.Send(t, page, url.Values{"prev_attachments": {"2"}, "attached_messages": {fmt.Sprintf("INBOX/%d", uid)}},
		"mail.message.forward", forward...)
	out := smtp.Last()
	if len(out) < 2*len(file) {
		t.Fatalf("%d bytes left, fewer than the file and the original weigh", len(out))
	}

	if stored := sentCopy(t, a.IMAP); stored != out {
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
	h.Set("To", "<"+User+">")
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

	smtp := SMTP(t)
	a := Alborz(t, WithSMTP(smtp))
	uid := a.IMAP.Append(t, "INBOX", original.String())
	c := a.SignIn(t, User)
	forward := []any{"INBOX", uid, url.Values{"part": {"1"}}}
	page := c.Page(t, "mail.message.forward", forward...)
	src := html.UnescapeString(Between(page, `<img src="`, `"`))
	if !strings.HasSuffix(src, "part=2") {
		t.Fatalf("the forward does not show the original's image: %q", src)
	}
	c.Send(t, page, url.Values{"html": {`<p>Look:</p><p><img src="` + html.EscapeString(src) + `"></p>`}},
		"mail.message.forward", forward...)

	e, err := message.Read(strings.NewReader(smtp.Last()))
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
		t.Fatalf("the forward left without the image:\n%s", headOf(smtp.Last()))
	}
}

// A message attached whole is never encoded (RFC 2046 5.2.1); one the way
// out cannot carry as it is goes as a file, its bytes intact.
func TestAttachedMessageTravelsAsItsBytesAllow(t *testing.T) {
	long := strings.Repeat("x", 998)
	head := "From: <eve@example.org>\r\nTo: <" + User + ">\r\nSubject: Whole\r\nMIME-Version: 1.0\r\n"
	sevenBit := head + "Content-Type: text/plain\r\n\r\nPlain.\r\n" + long + "\r\n"
	eightBit := head + "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\nسلام\r\n" + long + "\r\n"
	binary := head + "Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n\r\na\x00b\r\n"
	for _, tc := range []struct {
		name, original string
		hidden         []string
		mediaType, cte string
	}{
		{"7bit", sevenBit, nil, "message/rfc822", "7bit"},
		{"8bit", eightBit, nil, "message/rfc822", "8bit"},
		{"8bit without 8BITMIME", eightBit, []string{"8BITMIME"}, "application/octet-stream", "base64"},
		{"binary", binary, nil, "application/octet-stream", "base64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			smtp := SMTP(t, Hide(tc.hidden...))
			a := Alborz(t, WithSMTP(smtp))
			uid := a.IMAP.Append(t, "INBOX", tc.original)
			c := a.SignIn(t, User)
			c.Send(t, c.Page(t, "mail.compose"), url.Values{"attached_messages": {fmt.Sprintf("INBOX/%d", uid)}}, "mail.compose")
			out := smtp.Last()
			if out == "" {
				t.Fatal("nothing left")
			}
			if n := smtp.Sessions(); n != 1 {
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
				if cte := part.Header.Get("Content-Transfer-Encoding"); mediaType != tc.mediaType || cte != tc.cte {
					t.Errorf("the message went as %s, %s; want %s, %s", mediaType, cte, tc.mediaType, tc.cte)
				}
				if got, _ := io.ReadAll(part.Body); string(got) != tc.original {
					t.Errorf("the message arrived as %q", got)
				}
				return nil
			})
			if !found {
				t.Fatalf("the message left without the one attached:\n%s", headOf(out))
			}
			if kept := sentCopy(t, a.IMAP); kept != out {
				t.Errorf("the Sent copy (%d bytes) is not what left (%d bytes)", len(kept), len(out))
			}
		})
	}
}

// A whole message the mail server cannot hand over is not the SMTP server's
// refusal; the form comes back saying which it was.
func TestUnreadableOriginalIsNotASendRefusal(t *testing.T) {
	smtp := SMTP(t)
	a := Alborz(t, WithIMAP(IMAP(t, Caps(imap.CapIMAP4rev1), FailWholeFetch())), WithSMTP(smtp))
	uid := a.IMAP.Append(t, "INBOX", "From: <eve@example.org>\r\nSubject: Gone\r\n\r\nBody.\r\n")
	c := a.SignIn(t, User)
	resp := c.Compose(t, c.Page(t, "mail.compose"), url.Values{"attached_messages": {fmt.Sprintf("INBOX/%d", uid)}}, "mail.compose")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unreadable original answered %s, want 422", resp.Status)
	}
	notice := NoticeBar(resp.Text)
	if !strings.Contains(notice, "could not be read from the mail server") || strings.Contains(notice, "was not sent. The server said") {
		t.Errorf("the form does not say the original was unreadable:\n%s", notice)
	}
	if smtp.Sessions() != 0 {
		t.Errorf("the SMTP server was contacted for a message that could not be written")
	}
}

func sentCopy(t *testing.T, s *IMAPServer) string {
	t.Helper()
	ic := s.Dial(t)
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
	signed := "Content-Type: multipart/mixed; boundary=in\r\n\r\n" +
		"--in\r\nContent-Type: text/plain\r\n\r\nThe signed words.\r\n" +
		"--in\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"big.bin\"\r\n\r\n" + wrapped(bytes.Repeat([]byte{0xa5}, 1<<21)) + "--in--\r\n"
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, key, strings.NewReader(signed), nil); err != nil {
		t.Fatal(err)
	}
	raw := "From: Gil <gil@example.org>\r\nTo: <" + User + ">\r\nSubject: Signed and large\r\nMIME-Version: 1.0\r\n" +
		"Autocrypt: addr=gil@example.org; keydata=" + base64.StdEncoding.EncodeToString(public.Bytes()) + "\r\n" +
		"Content-Type: multipart/signed; micalg=pgp-sha256; protocol=\"application/pgp-signature\"; boundary=sig\r\n\r\n" +
		"--sig\r\n" + signed + "\r\n--sig\r\nContent-Type: application/pgp-signature\r\n\r\n" + sig.String() + "\r\n--sig--\r\n"

	a := Alborz(t)
	uid := a.IMAP.Append(t, "INBOX", raw)
	c := a.SignIn(t, User)
	before := a.IMAP.Wire()
	page := c.Page(t, "mail.message", "INBOX", uid)
	if moved := a.IMAP.Wire() - before; moved >= 1<<21 {
		t.Errorf("opening the message moved %d bytes, as much as its attachment", moved)
	}
	if !strings.Contains(page, "The signed words.") || !strings.Contains(page, a.Path("mail.message.signature", "INBOX", uid)) {
		t.Fatal("the page shows neither the signed words nor a place for the verdict")
	}
	if verdict := c.Page(t, "mail.message.signature", "INBOX", uid); !strings.Contains(verdict, "sig-good") {
		t.Errorf("the verdict on a good signature reads %q", verdict)
	}
}

func TestListCardOnlyWhenItHasRows(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	seen := map[string]bool{}
	for _, uid := range MessageUIDs(c.Page(t, "mail.mailbox", "INBOX")) {
		page := c.Page(t, "mail.message", "INBOX", uid, url.Values{"part": {"1"}})
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
	a := Alborz(t)
	c := a.SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	flag := func(next string) string {
		resp := c.Post(t, "mail.messages.flag", url.Values{"uids": {uids[0]}, "flags": {"\\Seen"}, "next": {next}}, "INBOX")
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("flag with next=%q: %s", next, resp.Status)
		}
		return resp.Header.Get("Location")
	}
	ours := a.Path("mail.mailbox", "INBOX", url.Values{"all": {"1"}})
	if to := flag(ours); to != ours {
		t.Errorf("a page of ours was not returned to: %q", to)
	}
	for _, next := range []string{"https://evil.example/", "//evil.example/", "/\\evil.example/"} {
		if to := flag(next); to != a.Path("mail.message", "INBOX", uids[0]) {
			t.Errorf("next=%q redirected to %q", next, to)
		}
	}
}

func TestRefusedRecipientComesBackOnTheForm(t *testing.T) {
	smtp := SMTP(t)
	c := Alborz(t, WithSMTP(smtp)).SignIn(t, User)

	resp := c.Compose(t, c.Page(t, "mail.compose"), url.Values{"to": {"refused@example.org"}, "subject": {"kept"}}, "mail.compose")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a refused recipient answered %s, want 422", resp.Status)
	}
	if !strings.Contains(NoticeBar(resp.Text), "User unknown") {
		t.Errorf("the server's refusal is not on the form:\n%s", resp.Text)
	}
	if !strings.Contains(resp.Text, `value="kept"`) {
		t.Errorf("the typed subject did not survive the refusal")
	}
	if smtp.Last() != "" {
		t.Errorf("a refused message reached the wire:\n%s", headOf(smtp.Last()))
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

// An undo with an undo would loop.
func TestMoveNoticeUndoes(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) == 0 {
		t.Fatal("the rig seeded no messages")
	}
	c.Post(t, "mail.messages.move", url.Values{"uids": {uids[0]}, "to": {"Archive"}}, "INBOX")
	action, fields := NoticeAction(c.Page(t, "mail.mailbox", "INBOX"))
	if fields.Get("undo") == "" {
		t.Fatalf("a move's notice offers no undo: %q %v", action, fields)
	}
	if resp := c.PostTo(t, action, fields); resp.StatusCode != http.StatusFound {
		t.Fatalf("undo: %s", resp.Status)
	}
	body := c.Page(t, "mail.mailbox", "INBOX")
	if n := len(MessageUIDs(body)); n != len(uids) {
		t.Errorf("INBOX holds %d after the undo, want %d", n, len(uids))
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Archive"))); n != 0 {
		t.Errorf("Archive still holds %d after the undo", n)
	}
	if action, _ := NoticeAction(body); action != "" {
		t.Errorf("the undo's notice offers %q", action)
	}
}

func TestSearchMoveUndoesIntoTheFolderItCameFrom(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	inbox, nested := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX")), MessageUIDs(c.Page(t, "mail.mailbox", "INBOX/Lists"))
	if len(nested) == 0 {
		t.Fatal("the rig seeded no messages in INBOX/Lists")
	}
	results := a.Path("mail.search", url.Values{"query": {"from:eve in:lists"}})
	c.Post(t, "mail.role.act", url.Values{"refs": {User + "|INBOX/Lists|" + nested[0]}, "next": {results}},
		"INBOX", url.Values{"action": {"move"}, "to": {"Archive"}})
	moved := c.Page(t, "mail.mailbox", "INBOX/Lists")
	if n := len(MessageUIDs(moved)); n != len(nested)-1 {
		t.Fatalf("INBOX/Lists holds %d after the move, want %d", n, len(nested)-1)
	}
	action, fields := NoticeAction(moved)
	if fields.Get("undo") == "" {
		t.Fatalf("the move's notice offers no undo: %q %v", action, fields)
	}
	c.PostTo(t, action, fields)
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "INBOX/Lists"))); n != len(nested) {
		t.Errorf("INBOX/Lists holds %d after the undo, want %d", n, len(nested))
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))); n != len(inbox) {
		t.Errorf("INBOX holds %d after the undo, want %d", n, len(inbox))
	}
}

func TestDroppedTwiceIsImportedOnce(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	eml := "From: ann@example.org\r\nTo: " + User + "\r\nSubject: dropped\r\n" +
		"Message-ID: <dropped@test>\r\nContent-Type: text/plain\r\n\r\nBody.\r\n"
	for range 2 {
		resp := c.Upload(t, "mail.mailbox.import", nil, []File{{Field: "file", Name: "dropped.eml", Body: strings.NewReader(eml)}}, "Archive")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("drop: %s", resp.Status)
		}
	}
	if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Archive"))); n != 1 {
		t.Errorf("Archive holds %d after the same file dropped twice, want 1", n)
	}
}

func TestDeleteNoticeCountsTheRest(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) < 3 {
		t.Fatalf("need three seeded messages, found %d", len(uids))
	}
	c.Post(t, "mail.messages.delete", url.Values{"uids": uids[:2]}, "INBOX", url.Values{"ipp": {"2"}})
	body := c.Page(t, "mail.mailbox", "INBOX")
	action, fields := NoticeAction(body)
	if fields != nil || action != a.Path("mail.mailbox.empty", "INBOX") {
		t.Fatalf("a full page's notice offers %q %v", action, fields)
	}
	rest := fmt.Sprintf("%d", len(uids)-2)
	if !strings.Contains(body, "Delete all "+rest+" in this folder") {
		t.Errorf("the notice does not count the rest: %s", NoticeBar(body))
	}
	if page := c.Follow(t, action); !strings.Contains(page, "It holds "+rest+" messages") {
		t.Errorf("the empty page does not say what it holds: %s", AlertText(page))
	}
	c.Post(t, "mail.messages.delete", url.Values{"uids": uids[2:3]}, "INBOX", url.Values{"ipp": {"2"}})
	if action, _ := NoticeAction(c.Page(t, "mail.mailbox", "INBOX")); action != "" {
		t.Errorf("a partial page's notice offers %q", action)
	}
}

func bothAccounts(t *testing.T, a *App) *Client {
	t.Helper()
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	return c
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
	a := Alborz(t)
	c := bothAccounts(t, a)
	for _, list := range [][]any{{"mail.mailbox", "INBOX"}, {"mail.search", url.Values{"query": {"example"}}}} {
		route, args := list[0].(string), list[1:]
		all := rowRefs(c.Page(t, route, append(args, url.Values{"ipp": {"100"}})...))
		if len(all) < 5 {
			t.Fatalf("%s: need five seeded rows to have a third page, found %d", route, len(all))
		}
		body := c.Page(t, route, append(args, url.Values{"ipp": {"2"}, "page": {"1"}})...)
		if got := rowRefs(body); !slices.Equal(got, all[2:4]) {
			t.Errorf("%s: the second page holds %v, want %v", route, got, all[2:4])
		}
		if want := fmt.Sprintf("3–4 of %d", len(all)); !strings.Contains(body, want) {
			t.Errorf("%s: the second page does not say %q", route, want)
		}
		if got := PagerSteps(t, body); !slices.Equal(got, []string{"", "2"}) {
			t.Errorf("%s: the second page steps to pages %q, want the first and the third", route, got)
		}
	}
}

// A message opened from the merged inbox walks the merged rows, across
// accounts, reading past the page it was on for the row after its last.
func TestAMergedListIsWalkedInItsOwnOrder(t *testing.T) {
	a := Alborz(t)
	c := bothAccounts(t, a)
	all := rowRefs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"ipp": {"100"}}))
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
		_, values, ok := a.Server.Match(u)
		if !ok {
			t.Fatalf("neighbour link %q is no route", m[1])
		}
		return u.Query().Get("account") + "|" + values["mbox"] + "|" + values["uid"]
	}
	// The last row of the second page, whose next is on the third, and a row inside one page.
	for _, at := range []struct {
		list     url.Values
		row, pos int
	}{{url.Values{"ipp": {"2"}, "page": {"1"}}, 1, 3}, {url.Values{"ipp": {"100"}}, 4, 4}} {
		links := subject.FindAllStringSubmatch(c.Page(t, "mail.mailbox", "INBOX", at.list), -1)
		if len(links) <= at.row {
			t.Fatalf("%v lists %d rows", at.list, len(links))
		}
		msg := c.Follow(t, html.UnescapeString(links[at.row][1]))
		if prev, next := step(msg, "k"), step(msg, "j"); prev != all[at.pos-1] || next != all[at.pos+1] {
			t.Errorf("%v: row %d steps to %q and %q, want %q and %q", at.list, at.row, prev, next, all[at.pos-1], all[at.pos+1])
		}
		if !strings.Contains(msg, fmt.Sprintf(">%d of 6<", at.pos+1)) {
			t.Errorf("%v: row %d does not say it is %d of 6", at.list, at.row, at.pos+1)
		}
	}
}

// A folder that is no role is merged from the accounts that hold it; one
// that no account holds is not there, rather than an empty list.
func TestAMergedFolderByNameListsWhoHoldsIt(t *testing.T) {
	c := bothAccounts(t, Alborz(t))
	refs := rowRefs(c.Page(t, "mail.mailbox", "INBOX/Lists"))
	if len(refs) != 5 {
		t.Fatalf("the merged INBOX/Lists lists %v, want the five seeded messages", refs)
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref, User+"|INBOX/Lists|") {
			t.Errorf("row %q is not from the account holding the folder's mail", ref)
		}
	}
	if resp := c.Get(t, "mail.mailbox", "Nowhere"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a folder no account holds answered %s, want 404", resp.Status)
	}
}

func TestAServerDownCostsItsRowsNotThePage(t *testing.T) {
	elsewhere := IMAP(t)
	a := Alborz(t, Upstream("elsewhere.local", elsewhere.Upstream()))
	c := a.SignIn(t, User)
	c.SignIn(t, Elsewhere)
	if rows := rowAccounts(c.Page(t, "mail.mailbox", "INBOX")); rows[User] == 0 || rows[Elsewhere] == 0 {
		t.Fatalf("the merged inbox is not both accounts: %v", rows)
	}

	elsewhere.Close()
	for _, list := range [][]any{{"mail.mailbox", "INBOX"}, {"mail.search", url.Values{"query": {"example"}}}} {
		route := list[0].(string)
		// The loss is noticed a moment after Close returns; until then the cache answers.
		var body string
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			c.Post(t, "mail.mailbox.refresh", nil, "INBOX")
			body = c.Page(t, route, list[1:]...)
			if strings.Contains(body, "did not answer") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: nothing says a server did not answer: %s", route, NoticeBar(body))
			}
		}
		if !strings.Contains(NoticeBar(body), Elsewhere) {
			t.Errorf("%s: the notice does not name the account: %s", route, NoticeBar(body))
		}
		if rows := rowAccounts(body); rows[User] == 0 || rows[Elsewhere] != 0 {
			t.Errorf("%s: rows by account are %v", route, rows)
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
	c := bothAccounts(t, Alborz(t))
	merged := rowAccounts(c.Page(t, "mail.mailbox", "INBOX"))
	if merged[User] == 0 || merged[User2] == 0 {
		t.Fatalf("the merged inbox is not both accounts: %v", merged)
	}
	for account, other := range map[string]string{User: User2, User2: User} {
		scoped := c.Page(t, "mail.mailbox", "INBOX", url.Values{"account": {account}})
		if n := len(MessageUIDs(scoped)); n == 0 {
			t.Errorf("%s has no rows when the page is scoped to it", account)
		}
		if strings.Contains(scoped, `value="`+other+`|`) {
			t.Errorf("a page scoped to %s shows %s's mail", account, other)
		}
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

func perPage(t *testing.T, c *Client) string {
	t.Helper()
	m := regexp.MustCompile(`id="messages_per_page"\s+value="(\d+)"`).FindStringSubmatch(c.Page(t, "settings.profile"))
	if m == nil {
		t.Fatal("the settings page shows no page size")
	}
	return m[1]
}

func TestProfileReachesEveryAccount(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	if resp := c.Post(t, "settings.profile", url.Values{"messages_per_page": {"17"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the profile: %s", resp.Status)
	}
	for _, account := range []string{User, User2} {
		if got := perPage(t, a.SignIn(t, account)); got != "17" {
			t.Errorf("%s holds a page size of %s, want 17", account, got)
		}
	}
}

func TestSigningInAskedAboutAnotherProfile(t *testing.T) {
	for _, answer := range []struct{ route, want string }{
		{"settings.profile.take", "17"},
		{"settings.profile.discard", "50"},
	} {
		t.Run(answer.route, func(t *testing.T) {
			a := Alborz(t)
			a.SignIn(t, User).Post(t, "settings.profile", url.Values{"messages_per_page": {"17"}})

			c := a.SignIn(t, User2)
			resp := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}}, url.Values{"add": {"1"}})
			bar := NoticeBar(c.Follow(t, resp.Header.Get("Location")))
			if !strings.Contains(bar, "Messages per page") || !strings.Contains(bar, "17") || !strings.Contains(bar, "50") {
				t.Fatalf("no notice names the differing page size:\n%s", bar)
			}
			if got := perPage(t, c); got != "50" {
				t.Fatalf("the session took %s before the reader answered", got)
			}

			var fields url.Values
			for _, f := range regexp.MustCompile(`(?s)<form method="post" action="([^"]+)" class="notice-action">(.*?)</form>`).FindAllStringSubmatch(bar, -1) {
				if f[1] != a.Path(answer.route) {
					continue
				}
				fields = url.Values{}
				for _, in := range regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(f[2], -1) {
					fields.Add(in[1], html.UnescapeString(in[2]))
				}
			}
			if fields == nil {
				t.Fatalf("the notice offers no %s:\n%s", answer.route, bar)
			}
			if resp := c.Post(t, answer.route, fields); resp.StatusCode != http.StatusFound {
				t.Fatalf("answering: %s", resp.Status)
			}

			if got := perPage(t, c); got != answer.want {
				t.Errorf("the session reads %s, want %s", got, answer.want)
			}
			for _, account := range []string{User, User2} {
				if got := perPage(t, a.SignIn(t, account)); got != answer.want {
					t.Errorf("%s holds %s, want %s", account, got, answer.want)
				}
			}
		})
	}
}

func TestCachingOffServesEveryPage(t *testing.T) {
	c := Alborz(t, NoCache(), WithDAV(DAV(t, Calendar(DAVHome+"personal/", "Personal")))).SignIn(t, User)

	if len(MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))) == 0 {
		t.Fatal("the inbox listed no messages")
	}
	unread := url.Values{"view": {"unread"}}
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", unread))
	if len(uids) == 0 {
		t.Fatal("no unread message to open")
	}
	uid := uids[0]
	if page := c.Page(t, "mail.message", "INBOX", uid, url.Values{"part": {"1"}}); !strings.Contains(page, "message-flag-form") {
		t.Fatal("the message page did not render")
	}
	if slices.Contains(MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", unread)), uid) {
		t.Errorf("message %s is still unread after it was opened", uid)
	}
	if !strings.Contains(c.Page(t, "calendar.month"), "Personal") {
		t.Error("the calendar page does not show the server's calendar")
	}
}

// A part past 2 MiB shows its start, cut on a whole rune or tag, and
// no more than that crosses the wire; the link fetches all of it.
func TestLongPartShowsItsStart(t *testing.T) {
	const shown = 2 << 20
	// 57 bytes to a base64 line, and an odd start: the cut splits an "é".
	text := "PARTSTART\r\n" + strings.Repeat(strings.Repeat("é", 1000)+"\r\n", 1600) + "PARTEND\r\n"
	// One line, and the cut falls inside a tag.
	markup := "<p>PARTSTART</p>" + strings.Repeat("<b>w</b>", (shown-40)/8) + `<a href="https://example.org/">link</a>` +
		strings.Repeat("<i>w</i>", 1<<17) + "<p>PARTEND</p>"

	for name, part := range map[string]struct{ header, body string }{
		"text": {"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n", wrapped([]byte(text))},
		"html": {"Content-Type: text/html; charset=utf-8\r\n", markup},
	} {
		t.Run(name, func(t *testing.T) {
			a := Alborz(t)
			uid := a.IMAP.Append(t, "INBOX", "From: <eve@example.org>\r\nTo: <"+User+">\r\nSubject: Long\r\nMIME-Version: 1.0\r\n"+
				part.header+"\r\n"+part.body)
			c := a.SignIn(t, User)
			before := a.IMAP.Wire()
			page := c.Page(t, "mail.message", "INBOX", uid, url.Values{"part": {"1"}})
			if moved := a.IMAP.Wire() - before; moved > shown+64<<10 {
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
			whole := a.Path("mail.message.raw", "INBOX", uid, url.Values{"part": {"1"}})
			if !slices.Contains(a.RawParts(page), whole) {
				t.Fatalf("no link to the whole part among %q", a.RawParts(page))
			}
			if body := c.Follow(t, whole); !strings.HasSuffix(body, "PARTEND\r\n") && !strings.HasSuffix(body, "<p>PARTEND</p>") {
				t.Error("the download is not the whole part")
			}
		})
	}
}

func TestUnbrokenLineShowsAsText(t *testing.T) {
	a := Alborz(t)
	line := "LINESTART" + strings.Repeat(" word", 100<<10/5) + " LINEEND"
	uid := a.IMAP.Append(t, "INBOX", "From: <eve@example.org>\r\nTo: <"+User+">\r\nSubject: One line\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n\r\n"+line+"\r\n")
	page := a.SignIn(t, User).Page(t, "mail.message", "INBOX", uid, url.Values{"part": {"1"}})
	if !strings.Contains(page, "LINESTART") || !strings.Contains(page, "LINEEND") {
		t.Error("a 100 KiB line does not show as text")
	}
}

// A conversation list is threaded once for all its pages, and a message
// opened from it walks the conversations' order, not the folder's.
func TestAConversationListThreadsOnceAndItsMessagesWalkIt(t *testing.T) {
	a := Alborz(t, WithIMAP(IMAP(t, Sorting())))
	c := a.SignIn(t, User)
	var order, links []string
	for page := range 3 {
		body := c.Page(t, "mail.mailbox", "INBOX", url.Values{"sort": {"thread"}, "ipp": {"1"}, "page": {fmt.Sprint(page)}})
		order = append(order, rowUIDs(body)...)
		for _, m := range regexp.MustCompile(`class="message-list-subject[^"]*" role="cell">\s*<a [^>]*href="([^"]+)"`).FindAllStringSubmatch(body, -1) {
			links = append(links, m[1])
		}
	}
	if len(order) != 5 || len(links) != 5 || slices.IsSortedFunc(order, func(a, b string) int { return strings.Compare(b, a) }) {
		t.Fatalf("three pages of one conversation each list %v, want the five seeded messages out of the folder's order", order)
	}
	msg := c.Follow(t, html.UnescapeString(links[2]))
	if prev, next := neighbourUID(t, msg, "k"), neighbourUID(t, msg, "j"); prev != order[1] || next != order[3] {
		t.Errorf("the third message steps to %q and %q, want %q and %q", prev, next, order[1], order[3])
	}
	if n := a.IMAP.Threads(); n != 1 {
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
	a := Alborz(t, WithIMAP(IMAP(t, Sorting())))
	c := a.SignIn(t, User)
	page := func(n int) []string {
		return rowUIDs(c.Page(t, "mail.mailbox", "INBOX", url.Values{"sort": {"subject"}, "dir": {"asc"}, "ipp": {"2"}, "page": {fmt.Sprint(n)}}))
	}
	var seen []string
	for n := range 3 {
		uids := page(n)
		if len(uids) == 0 {
			t.Fatalf("page %d of the sorted inbox lists no rows", n)
		}
		seen = append(seen, uids...)
	}
	if len(seen) != 5 || len(slices.Compact(slices.Sorted(slices.Values(seen)))) != 5 {
		t.Fatalf("three pages of two list %v, want the five seeded messages once each", seen)
	}
	if n := a.IMAP.Sorts(); n != 1 {
		t.Errorf("three pages of one order sent %d SORTs, want 1", n)
	}
	if resp := c.Post(t, "mail.messages.move", url.Values{"uids": {seen[0]}, "to": {"Archive"}}, "INBOX"); resp.StatusCode != http.StatusFound {
		t.Fatalf("move: %s", resp.Status)
	}
	if uids := page(1); slices.Contains(uids, seen[0]) || len(uids) == 0 {
		t.Errorf("the second page after a move lists %v", uids)
	}
	if n := a.IMAP.Sorts(); n != 2 {
		t.Errorf("a page after a move sent %d SORTs in all, want 2", n)
	}
}

// A message opened from the third page walks to the rows either side of it in
// that order, taken from the order the pages were cut from.
func TestNeighboursOnTheThirdPageOfASortedList(t *testing.T) {
	a := Alborz(t, WithIMAP(IMAP(t, Sorting())))
	c := a.SignIn(t, User)
	sorted := url.Values{"sort": {"subject"}, "dir": {"desc"}}
	order := rowUIDs(c.Page(t, "mail.mailbox", "INBOX", sorted, url.Values{"ipp": {"100"}}))
	if len(order) != 5 {
		t.Fatalf("the sorted inbox lists %v, want the five seeded messages", order)
	}
	page := c.Page(t, "mail.mailbox", "INBOX", sorted, url.Values{"ipp": {"1"}, "page": {"2"}})
	link := regexp.MustCompile(`class="message-list-subject[^"]*" role="cell">\s*<a [^>]*href="([^"]+)"`).FindStringSubmatch(page)
	if link == nil {
		t.Fatal("the third page has no row to open")
	}
	msg := c.Follow(t, html.UnescapeString(link[1]))
	if prev, next := neighbourUID(t, msg, "k"), neighbourUID(t, msg, "j"); prev != order[1] || next != order[3] {
		t.Errorf("the third message steps to %q and %q, want %q and %q", prev, next, order[1], order[3])
	}
	if !strings.Contains(msg, ">3 of 5<") {
		t.Error("the third message does not say it is 3 of 5")
	}
	if n := a.IMAP.Sorts(); n != 1 {
		t.Errorf("listing and opening sent %d SORTs, want the one", n)
	}
}

// Blocking lands wherever the form says, not on a route, and leaves the rule in the script.
func TestBlockingASenderLandsOnTheMessage(t *testing.T) {
	sieve := Sieve(t)
	a := Alborz(t, WithSieve(sieve), Upstream("elsewhere.local", IMAP(t).Upstream()), Upstream("elsewhere.local", sieve.Upstream()))
	c := a.SignIn(t, User)
	inbox := a.Path("mail.mailbox", "INBOX")
	resp := c.Post(t, "filters.rules.block", url.Values{"value": {"spam@example.org"}, "next": {inbox}}, url.Values{"account": {User}})
	if at := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || at != inbox {
		t.Fatalf("block landed on %q with %s, want %s", at, resp.Status, inbox)
	}
	if !strings.Contains(c.Follow(t, inbox), "Mail from spam@example.org now goes to Junk.") {
		t.Errorf("the landing page does not say the sender was blocked")
	}
	if rules := sieve.Script("alborz-rules"); !strings.Contains(rules, `address :is "from" "spam@example.org"`) {
		t.Errorf("the rules script does not block the sender:\n%s", rules)
	}

	// Off-site is refused on either landing; the account elsewhere has no junk folder to block into.
	for account, c := range map[string]*Client{User: c, Elsewhere: a.SignIn(t, Elsewhere)} {
		for _, next := range []string{"https://evil.example/", "//evil.example/"} {
			resp := c.Post(t, "filters.rules.block", url.Values{"value": {"spam@example.org"}, "next": {next}}, url.Values{"account": {account}})
			if at := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || !strings.HasPrefix(at, a.Path("filters.rules")+"?") {
				t.Errorf("%s blocking with next %s: %s to %q, want the rules page", account, next, resp.Status, at)
			}
		}
	}
}
