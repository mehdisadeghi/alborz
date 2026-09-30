package web_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message"
)

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
