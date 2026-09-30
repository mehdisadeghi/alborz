package web_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"io"
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
