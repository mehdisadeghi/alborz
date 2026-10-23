package web_test

import (
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/emersion/go-message"
)

func TestComposeAttributionIsSeparateFromSignaturesAndDrafts(t *testing.T) {
	smtp := SMTP(t)
	c := Alborz(t, WithSMTP(smtp)).SignIn(t, User)
	const footer = "Sent by Alborz Webmail"
	page := c.Page(t, "mail.compose")
	if strings.Contains(page, footer) {
		t.Fatal("attribution leaked into the editor")
	}
	for _, rich := range []bool{false, true} {
		fields := url.Values{"text": {"Hello\n\n-- \nRegards"}}
		if rich {
			fields.Set("html", "<p>Hello</p><p>Regards</p>")
		}
		c.Send(t, c.Page(t, "mail.compose"), fields, "mail.compose")
		e, err := message.Read(strings.NewReader(smtp.Last()))
		if err != nil {
			t.Fatal(err)
		}
		parts := 0
		if err := e.Walk(func(_ []int, part *message.Entity, err error) error {
			if err != nil {
				return err
			}
			kind, _, _ := part.Header.ContentType()
			if kind != "text/plain" && kind != "text/html" {
				return nil
			}
			body, err := io.ReadAll(part.Body)
			if err != nil {
				return err
			}
			if strings.Count(string(body), footer) != 1 || !strings.Contains(string(body), "Regards") {
				t.Errorf("%s lost the signature or has wrong attribution count: %s", kind, body)
			}
			parts++
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if parts == 0 {
			t.Fatal("no outgoing text part")
		}
	}
	draft := c.Compose(t, page, url.Values{"save_as_draft": {"1"}, "text": {"Draft body"}}, "mail.compose")
	if draft.StatusCode >= 400 {
		t.Fatal(draft.Status)
	}
	if strings.Contains(c.Follow(t, draft.Header.Get("Location")), footer) {
		t.Fatal("draft contains attribution")
	}
	refused := c.Compose(t, c.Page(t, "mail.compose"), url.Values{"to": {"refused@example.org"}, "text": {"Retry body"}}, "mail.compose")
	if refused.StatusCode != http.StatusUnprocessableEntity || strings.Contains(refused.Text, footer) {
		t.Fatalf("refused send polluted the editor: %s", refused.Status)
	}
	text := composeText.FindStringSubmatch(refused.Text)
	if text == nil {
		t.Fatal("refused send lost its body")
	}
	c.Send(t, refused.Text, url.Values{"text": {html.UnescapeString(text[1])}}, "mail.compose")
	if strings.Count(smtp.Last(), footer) != 1 {
		t.Fatal("retry duplicated attribution")
	}
	if r := c.Post(t, "settings.profile", url.Values{"writing_preferences": {"1"}, "include_attribution": {""}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	c.Send(t, c.Page(t, "mail.compose"), url.Values{"text": {"No footer"}}, "mail.compose")
	if strings.Contains(smtp.Last(), footer) {
		t.Fatal("disabled attribution was sent")
	}
}

func TestComposeEntryLinksKeepAccountScope(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	for _, account := range []string{"", User, User2} {
		page := c.Page(t, "mail.mailbox", "INBOX", url.Values{"account": {account}})
		found := false
		for _, link := range regexp.MustCompile(`href="([^"]+)"`).FindAllStringSubmatch(page, -1) {
			u, err := url.Parse(html.UnescapeString(link[1]))
			if err != nil {
				t.Fatal(err)
			}
			if u.Path != a.Path("mail.compose") {
				continue
			}
			found = true
			if got := u.Query().Get("account"); got != account {
				t.Errorf("compose link scope = %q, want %q", got, account)
			}
		}
		if !found {
			t.Fatal("mailbox has no compose link")
		}
	}
	r := c.Get(t, "mail.compose", url.Values{"account": {User2}, "mailto": {"mailto:friend@example.org"}})
	u, err := url.Parse(r.Header.Get("Location"))
	if err != nil || u.Query().Get("account") != User2 {
		t.Fatalf("mailto lost account: %s", r.Header.Get("Location"))
	}
}

func TestPooledSignaturesKeepTheirAccountAndDefaults(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	c.SignIn(t, User2)
	for _, account := range []string{User, User2} {
		q := url.Values{"account": {account}}
		if r := c.Post(t, "mail.signatures", url.Values{"name": {"Work"}, "text": {"Regards from " + account}}, q); r.StatusCode != http.StatusFound {
			t.Fatal(r.Status)
		}
		if r := c.Post(t, "mail.signatures.default", url.Values{"name": {"Work"}}, q); r.StatusCode != http.StatusFound {
			t.Fatal(r.Status)
		}
	}
	q := url.Values{"account": {User2}}
	page := c.Page(t, "mail.compose", q)
	if strings.Contains(page, "Regards from "+User+"<") {
		t.Fatal("another account's default selected")
	}
	if r := c.Post(t, "settings.profile", url.Values{"writing_preferences": {"1"}, "pool_signatures": {"1"}, "include_attribution": {"1"}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	page = c.Page(t, "mail.compose", q)
	opts := regexp.MustCompile(`<option value="([^"]+)"[^>]*>Work · ` + regexp.QuoteMeta(User) + `</option>`).FindStringSubmatch(page)
	if opts == nil {
		t.Fatal("pooled signature missing its account label")
	}
	chosen := html.UnescapeString(opts[1])
	r := c.Compose(t, page, url.Values{"signature_apply": {"1"}, "signature": {chosen}, "text": {"Hello"}}, "mail.compose", q)
	if r.StatusCode != http.StatusOK || !strings.Contains(r.Text, "Regards from "+User) {
		t.Fatalf("pooled signature not applied: %s", r.Status)
	}
	if r := c.Post(t, "settings.profile", url.Values{"writing_preferences": {"1"}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	r = c.Compose(t, c.Page(t, "mail.compose", q), url.Values{"signature_apply": {"1"}, "signature": {chosen}, "text": {"Hello"}}, "mail.compose", q)
	if r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(r.Text, "Hello") {
		t.Fatalf("cross-account signature accepted with pooling disabled: %s", r.Status)
	}
	if page := c.Page(t, "mail.compose", q); !strings.Contains(page, "Regards from "+User2) {
		t.Fatal("pooling changed the account default")
	}
}
