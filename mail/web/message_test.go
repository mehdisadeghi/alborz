package web_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-mbox"
	nethtml "golang.org/x/net/html"
)

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

// Over BINARY (RFC 3516) the server decodes the part and knows its length, so the file arrives
// with it, byte for byte.
func TestAnAttachmentDownloadsAsItsBytes(t *testing.T) {
	file := make([]byte, 100<<10)
	for i := range file {
		file[i] = byte(i * 7)
	}
	raw := "From: <eve@example.org>\r\nTo: <" + User + ">\r\nSubject: A file\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nSee the file.\r\n" +
		"--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"file.bin\"\r\n\r\n" + wrapped(file) + "--b--\r\n"
	a := Alborz(t)
	uid := a.IMAP.Append(t, "INBOX", raw)
	resp := a.SignIn(t, User).Get(t, "mail.message.raw", "INBOX", uid, url.Values{"part": {"2"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the download answered %s", resp.Status)
	}
	if got := resp.Header.Get("Content-Length"); got != fmt.Sprint(len(file)) {
		t.Errorf("Content-Length is %q, want %d", got, len(file))
	}
	if !bytes.Equal([]byte(resp.Text), file) {
		t.Errorf("the download is %d bytes unlike the %d-byte file", len(resp.Text), len(file))
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

// frameDoc is the document the message page puts in the mail frame.
func frameDoc(t *testing.T, page string) string {
	t.Helper()
	doc := html.UnescapeString(Between(page, `srcdoc="`, `"`))
	if !strings.Contains(doc, "MARK") {
		t.Fatalf("the frame does not hold the message:\n%s", doc)
	}
	return doc
}

var (
	refusedElements = []string{"script", "iframe", "frame", "object", "embed", "applet", "link", "meta", "base",
		"form", "input", "button", "textarea", "select", "svg", "math"}
	cssFetch = regexp.MustCompile(`(?i)url\(|expression\(|javascript:`)
)

// markupOf is each element and element@attribute=value in doc.
func markupOf(doc string) map[string]bool {
	kept := map[string]bool{}
	z := nethtml.NewTokenizer(strings.NewReader(doc))
	for tt := z.Next(); tt != nethtml.ErrorToken; tt = z.Next() {
		if tt != nethtml.StartTagToken && tt != nethtml.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		kept[tok.Data] = true
		for _, a := range tok.Attr {
			kept[tok.Data+"@"+a.Key+"="+a.Val] = true
		}
	}
	return kept
}

// unsafeMarkup is every way the frame document could run, fetch, or address what lies outside the
// message; images may come only from under the paths own.
func unsafeMarkup(doc string, own ...string) []string {
	var found []string
	z := nethtml.NewTokenizer(strings.NewReader(doc))
	for {
		tt := z.Next()
		if tt == nethtml.ErrorToken {
			return found
		}
		tok := z.Token()
		if tt == nethtml.TextToken {
			continue
		}
		if slices.Contains(refusedElements, tok.Data) {
			found = append(found, "<"+tok.Data+">")
		}
		if tok.Data == "style" && tt == nethtml.StartTagToken && z.Next() == nethtml.TextToken {
			if css := strings.ReplaceAll(string(z.Text()), "url(about:blank)", ""); cssFetch.MatchString(css) {
				found = append(found, "style sheet "+css)
			}
		}
		attrs := map[string]string{}
		for _, a := range tok.Attr {
			attrs[a.Key] = a.Val
			switch {
			case strings.HasPrefix(a.Key, "on"), a.Key == "srcdoc", a.Key == "formaction", a.Key == "action", strings.Contains(a.Key, ":"):
				found = append(found, tok.Data+"@"+a.Key)
			case a.Key == "src", a.Key == "background":
				if !slices.ContainsFunc(own, func(p string) bool { return strings.HasPrefix(a.Val, p) }) {
					found = append(found, tok.Data+"@"+a.Key+"="+a.Val)
				}
			case a.Key == "href", a.Key == "cite":
				if u, err := url.Parse(a.Val); err != nil || !slices.Contains([]string{"http", "https", "mailto"}, u.Scheme) {
					found = append(found, tok.Data+"@"+a.Key+"="+a.Val)
				}
			case a.Key == "style":
				if css := strings.ReplaceAll(a.Val, "url(about:blank)", ""); cssFetch.MatchString(css) {
					found = append(found, tok.Data+"@style="+a.Val)
				}
			}
		}
		if href, ok := attrs["href"]; ok && tok.Data == "a" {
			rel := strings.Fields(attrs["rel"])
			if !slices.Contains(rel, "nofollow") || !slices.Contains(rel, "noreferrer") {
				found = append(found, "a without rel nofollow noreferrer: "+href)
			}
			if u, _ := url.Parse(href); u.Host != "" && (attrs["target"] != "_blank" || !slices.Contains(rel, "noopener")) {
				found = append(found, "a to another host without target _blank and noopener: "+href)
			}
		}
	}
}

// Every way an incoming message might run script, load on its own or reach outside itself is
// refused, whatever the spelling (ADR 44).
func TestHostileHTMLIsNeutralised(t *testing.T) {
	cases := map[string]string{
		"script":                   `<script>alert(1)</script>`,
		"script upper case":        `<SCRIPT SRC=https://evil.example/x.js></SCRIPT>`,
		"onerror":                  `<img src="https://evil.example/x.png" onerror="alert(1)">`,
		"onload on body":           `<body onload="alert(1)"><p>x</p></body>`,
		"onmouseover upper case":   `<div ONMOUSEOVER="alert(1)">x</div>`,
		"javascript link":          `<a href="javascript:alert(1)">x</a>`,
		"javascript mixed case":    `<a href="JaVaScRiPt:alert(1)">x</a>`,
		"javascript entity":        `<a href="&#106;avascript:alert(1)">x</a>`,
		"javascript hex entity":    `<a href="&#x6A;avascript&#x3A;alert(1)">x</a>`,
		"javascript tab":           `<a href="java&#x09;script:alert(1)">x</a>`,
		"javascript newline":       "<a href=\"java\nscript:alert(1)\">x</a>",
		"javascript leading space": `<a href="  javascript:alert(1)">x</a>`,
		"vbscript":                 `<a href="vbscript:msgbox(1)">x</a>`,
		"data html link":           `<a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">x</a>`,
		"data svg image":           `<img src="data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+">`,
		"javascript image":         `<img src="javascript:alert(1)">`,
		"relative link":            `<a href="/logout">x</a>`,
		"protocol relative link":   `<a href="//evil.example/">x</a>`,
		"fragment link":            `<a href="#top">x</a>`,
		"target self":              `<a href="https://evil.example/" target="_self" rel="opener">x</a>`,
		"cite javascript":          `<blockquote cite="javascript:alert(1)">x</blockquote>`,
		"srcdoc":                   `<iframe srcdoc="<script>alert(1)</script>"></iframe>`,
		"iframe":                   `<iframe src="https://evil.example/"></iframe>`,
		"object":                   `<object data="https://evil.example/x.swf"></object>`,
		"embed":                    `<embed src="https://evil.example/x.swf">`,
		"applet":                   `<applet code="X.class" codebase="https://evil.example/"></applet>`,
		"link":                     `<link rel="stylesheet" href="https://evil.example/x.css">`,
		"meta refresh":             `<meta http-equiv="refresh" content="0;url=https://evil.example/">`,
		"base":                     `<base href="https://evil.example/"><a href="x">x</a>`,
		"form":                     `<form action="https://evil.example/"><input name="p"><button formaction="https://evil.example/">Go</button><textarea>t</textarea><select><option>o</option></select></form>`,
		"style url":                `<div style="background:url(https://evil.example/t.png)">x</div>`,
		"style url quoted":         `<div style="background-image:url('https://evil.example/t.png')">x</div>`,
		"style expression":         `<div style="width:expression(alert(1))">x</div>`,
		"sheet import url":         `<style>@import url(https://evil.example/x.css); p{color:red}</style>`,
		"sheet import string":      `<style>@import 'https://evil.example/x.css';</style>`,
		"sheet url":                `<style>p{background:url(https://evil.example/t.png)}</style>`,
		"sheet breaks out":         `<style>p{color:"</style><img src=x onerror=alert(1)>"}</style>`,
		"background javascript":    `<table background="javascript:alert(1)"><tr><td background="javascript:alert(1)">x</td></tr></table>`,
		"background vbscript":      `<table><tr><td background="vbscript:msgbox(1)">x</td></tr></table>`,
		"background remote":        `<table background="https://evil.example/t.png"><tr><td>x</td></tr></table>`,
		"background relative":      `<table><tr><td background="/logout">x</td></tr></table>`,
		"background protocol rel":  `<table><tr><td background="//evil.example/t.png">x</td></tr></table>`,
		"background data":          `<table><tr><td background="data:image/png;base64,iVBORw0KGgo=">x</td></tr></table>`,
		"svg script":               `<svg><script>alert(1)</script></svg>`,
		"svg onload":               `<svg onload="alert(1)"><circle r="1"/></svg>`,
		"svg xlink":                `<svg><a xlink:href="javascript:alert(1)"><text>x</text></a></svg>`,
		"math mxss":                `<math><mtext><table><mglyph><style><img src=x onerror=alert(1)></style></mglyph></table></mtext></math>`,
		"noscript mxss":            `<noscript><p title="</noscript><img src=x onerror=alert(1)>"></p></noscript>`,
	}
	a := Alborz(t)
	c := a.SignIn(t, User)
	for name, markup := range cases {
		t.Run(name, func(t *testing.T) {
			uid := a.IMAP.Append(t, "INBOX", "From: <eve@example.org>\r\nTo: <"+User+">\r\nSubject: Hostile\r\nMIME-Version: 1.0\r\n"+
				"Content-Type: text/html; charset=utf-8\r\n\r\n"+markup+"<p>MARK</p>\r\n")
			for _, remote := range []string{"", "1"} {
				page := c.Page(t, "mail.message", "INBOX", uid, url.Values{"part": {"1"}, "allow-remote-resources": {remote}})
				own := []string{a.Path("mail.message.raw", "INBOX", uid) + "?", a.Path("mail.proxy") + "?"}
				if found := unsafeMarkup(frameDoc(t, page), own...); len(found) > 0 {
					t.Errorf("remote %q: the frame keeps %q", remote, found)
				}
			}
		})
	}
}

// Mail laid out with presentational HTML keeps it, and a background resolves as an image does:
// to the message's own part, or through the proxy once remote images are allowed.
func TestPresentationalHTMLKept(t *testing.T) {
	const remote = "https://img.example.org/bg.png"
	markup := `<center><font color="#336699" size="+1" face="Georgia, serif">Big</font> <big>b</big> <small>s</small></center>` +
		`<p align="right">r</p><hr size="2" color="gray" width="80%" align="left" noshade>` +
		`<table width="600" cellpadding="4" cellspacing="0" border="0" bgcolor="#ffffff" align="center" background="cid:bg">` +
		`<tr><td valign="top" nowrap colspan="2" bgcolor="#eeeeee" width="50%" height="20" background="` + remote + `">MARK</td></tr></table>` +
		`<img src="cid:bg" border="0" width="10" alt="logo">`
	want := []string{"center", "big", "small", "font@color=#336699", "font@size=+1", "font@face=Georgia, serif", "p@align=right",
		"hr@size=2", "hr@color=gray", "hr@width=80%", "hr@align=left", "hr@noshade=",
		"table@width=600", "table@cellpadding=4", "table@cellspacing=0", "table@border=0", "table@bgcolor=#ffffff", "table@align=center",
		"td@valign=top", "td@nowrap=", "td@colspan=2", "td@bgcolor=#eeeeee", "td@width=50%", "td@height=20",
		"img@border=0", "img@width=10", "img@alt=logo"}

	a := Alborz(t)
	uid := a.IMAP.Append(t, "INBOX", "From: <eve@example.org>\r\nTo: <"+User+">\r\nSubject: Layout\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: multipart/related; boundary=r\r\n\r\n"+
		"--r\r\nContent-Type: text/html; charset=utf-8\r\n\r\n"+markup+"\r\n"+
		"--r\r\nContent-Type: image/png\r\nContent-ID: <bg>\r\n\r\nPNG\r\n--r--\r\n")
	c := a.SignIn(t, User)
	part := a.Path("mail.message.raw", "INBOX", uid, url.Values{"part": {"2"}})
	proxied := `background="` + a.Path("mail.proxy") + `?src=`

	doc := frameDoc(t, c.Page(t, "mail.message", "INBOX", uid, url.Values{"part": {"1"}}))
	kept := markupOf(doc)
	for _, w := range append(want, "table@background="+part, "img@src="+part) {
		if !kept[w] {
			t.Errorf("the frame lost %s:\n%s", w, doc)
		}
	}
	if strings.Contains(doc, proxied) || strings.Contains(doc, remote) {
		t.Errorf("a remote background loads before remote images are allowed:\n%s", doc)
	}

	doc = frameDoc(t, c.Page(t, "mail.message", "INBOX", uid, url.Values{"part": {"1"}, "allow-remote-resources": {"1"}}))
	if !strings.Contains(doc, proxied) {
		t.Errorf("an allowed remote background does not go through the proxy:\n%s", doc)
	}
}

// A transfer encoding go-message lacks leaves the part as its bytes: the page, a reply and the zip
// show it as sent, and the parts beside it as usual.
func TestAnUnknownTransferEncodingShowsTheBytes(t *testing.T) {
	uuencoded := "begin 644 a\r\n#86)C\r\n`\r\nend\r\n"
	raw := "From: <eve@example.org>\r\nTo: <" + User + ">\r\nSubject: Old encodings\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8-bit\r\n\r\nHello from an old mailer.\r\n" +
		"--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: x-uuencode\r\n" +
		"Content-Disposition: attachment; filename=\"a.uue\"\r\n\r\n" + uuencoded +
		"--b\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"b.txt\"\r\n\r\nplain\r\n--b--\r\n"
	a := Alborz(t)
	uid := a.IMAP.Append(t, "INBOX", raw)
	c := a.SignIn(t, User)

	for _, route := range []string{"mail.message", "mail.message.reply", "mail.message.forward"} {
		resp := c.Get(t, route, "INBOX", uid, url.Values{"part": {"1"}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %s", route, resp.Status)
		}
		if !strings.Contains(resp.Text, "Hello from an old mailer.") {
			t.Errorf("%s does not show the text", route)
		}
	}

	resp := c.Get(t, "mail.message.attachments", "INBOX", uid)
	archive, err := zip.NewReader(strings.NewReader(resp.Text), int64(len(resp.Text)))
	if err != nil {
		t.Fatalf("attachments.zip (%s): %v", resp.Status, err)
	}
	got := map[string]string{}
	for _, f := range archive.File {
		r, err := f.Open()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		b, _ := io.ReadAll(r)
		got[f.Name] = string(b)
	}
	if want := map[string]string{"a.uue": strings.TrimSuffix(uuencoded, "\r\n"), "b.txt": "plain"}; !maps.Equal(got, want) {
		t.Errorf("the zip holds %q, want %q", got, want)
	}
}

// A Content-Type whose parameters do not parse is read as the server's structure has it.
func TestAMalformedContentTypeIsReadAsTheServerHasIt(t *testing.T) {
	head := "From: <eve@example.org>\r\nTo: <" + User + ">\r\nSubject: Odd parameters\r\nMIME-Version: 1.0\r\n"
	a := Alborz(t)
	c := a.SignIn(t, User)
	for _, contentType := range []string{`text/plain; charset="utf-8; format=flowed`, "text/plain; charset=utf-8; =bad"} {
		uid := a.IMAP.Append(t, "INBOX", head+"Content-Type: "+contentType+"\r\n\r\nSalam, donya.\r\n")
		for _, route := range []string{"mail.message", "mail.message.reply", "mail.message.forward"} {
			resp := c.Get(t, route, "INBOX", uid, url.Values{"part": {"1"}})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s under %q: %s", route, contentType, resp.Status)
			}
			if !strings.Contains(resp.Text, "Salam, donya.") {
				t.Errorf("%s under %q does not show the text", route, contentType)
			}
		}
	}
}

// A text part whose base64 breaks off is shown and quoted as far as it decodes, and the page says it is damaged.
func TestADamagedPartShowsWhatDecodes(t *testing.T) {
	head := "From: <eve@example.org>\r\nTo: <" + User + ">\r\nSubject: Damaged\r\nMIME-Version: 1.0\r\n"
	good := base64.StdEncoding.EncodeToString([]byte("The first line decodes.\n"))
	a := Alborz(t)
	c := a.SignIn(t, User)
	damaged := a.IMAP.Append(t, "INBOX", head+"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n"+
		good+"\r\nVGhl!!!!IHJlc3Q=\r\n")
	whole := a.IMAP.Append(t, "INBOX", head+"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n"+good+"\r\n")

	note := "This message part is damaged."
	for _, route := range []string{"mail.message", "mail.message.reply", "mail.message.forward"} {
		resp := c.Get(t, route, "INBOX", damaged, url.Values{"part": {"1"}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %s", route, resp.Status)
		}
		if !strings.Contains(resp.Text, "The first line decodes.") {
			t.Errorf("%s does not show what decoded", route)
		}
	}
	if page := c.Page(t, "mail.message", "INBOX", damaged, url.Values{"part": {"1"}}); !strings.Contains(page, note) {
		t.Error("the damaged part carries no note")
	}
	if page := c.Page(t, "mail.message", "INBOX", whole, url.Values{"part": {"1"}}); !strings.Contains(page, "The first line decodes.") || strings.Contains(page, note) {
		t.Error("a whole part is not shown plainly")
	}
}
