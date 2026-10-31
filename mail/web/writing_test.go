package web_test

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// The attribution closes the sign-off: under the signature, once after a switch, above a quote on top.
func TestComposeAttributionClosesTheSignOff(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	const footer = "Sent by Alborz Webmail"
	body := func(page string) string {
		m := composeText.FindStringSubmatch(page)
		if m == nil {
			t.Fatal("no compose body")
		}
		return html.UnescapeString(m[1])
	}
	if !strings.Contains(c.Page(t, "mail.compose"), "-- \n"+footer+"</textarea>") {
		t.Fatal("new mail lacks the attribution under its delimiter")
	}
	if r := c.Post(t, "mail.signatures", url.Values{"name": {"Work"}, "text": {"Regards"}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	apply := func(text, sig string, route string, args ...any) string {
		values := url.Values{"signature_apply": {"1"}, "signature": {sig}, "text": {text}}
		return body(c.Compose(t, c.Page(t, route, args...), values, route, args...).Text)
	}
	if got := apply("Hello\n\n-- \n"+footer, "Work", "mail.compose"); got != "Hello\n\n-- \nRegards\n"+footer {
		t.Fatalf("switching to a signature gave %q", got)
	}
	// Browsers submit textarea lines as CRLF.
	if got := apply("Hello\r\n\r\n-- \r\nRegards\r\n"+footer, "", "mail.compose"); got != "Hello\n\n-- \n"+footer {
		t.Fatalf("removing the signature gave %q", got)
	}
	uid := fmt.Sprint(a.IMAP.Append(t, "INBOX", "From: sender@example.org\r\nTo: "+User+"\r\nSubject: quoted\r\nContent-Type: text/plain\r\n\r\nOriginal words\r\n"))
	reply := body(c.Page(t, "mail.message.reply", "INBOX", uid))
	got := apply(reply, "Work", "mail.message.reply", "INBOX", uid)
	if strings.Count(got, footer) != 1 || !strings.Contains(got, "Regards\n"+footer) || strings.Index(got, footer) > strings.Index(got, "> Original words") {
		t.Fatalf("the reply's sign-off is not once above the quote: %q", got)
	}
	edited := strings.Replace(reply, footer, "Sent by me", 1)
	values := url.Values{"signature_apply": {"1"}, "signature": {"Work"}, "text": {edited}}
	r := c.Compose(t, c.Page(t, "mail.message.reply", "INBOX", uid), values, "mail.message.reply", "INBOX", uid)
	if got := body(r.Text); r.StatusCode != http.StatusUnprocessableEntity || got != strings.ReplaceAll(edited, "\r\n", "\n") {
		t.Fatalf("a switch under an edited sign-off changed the body: %s %q", r.Status, got)
	}
	rich := func(sign string) string {
		values := url.Values{"signature_apply": {"1"}, "signature": {"Work"}, "html": {`<p>Hello</p><p dir="rtl">-- <br>` + sign + `</p><blockquote><p>-- <br>Old</p></blockquote>`}}
		m := regexp.MustCompile(`name="html" value="([^"]*)"`).FindStringSubmatch(c.Compose(t, c.Page(t, "mail.compose"), values, "mail.compose").Text)
		if m == nil {
			t.Fatal("no rich compose body")
		}
		return html.UnescapeString(m[1])
	}
	if got := rich(footer); !strings.Contains(got, ">Hello</p>") || !strings.Contains(got, "-- <br>Regards<br>"+footer+"</p>") || !strings.Contains(got, "-- <br>Old") {
		t.Fatalf("rich sign-off not swapped in place: %q", got)
	}
	if got := rich("Sent by me"); strings.Contains(got, "Regards") || !strings.Contains(got, "Sent by me") {
		t.Fatalf("an edited rich sign-off was replaced: %q", got)
	}
	if r := c.Post(t, "settings.profile", url.Values{"writing_preferences": {"1"}, "include_attribution": {""}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	if strings.Contains(c.Page(t, "mail.compose"), footer) {
		t.Fatal("disabled attribution was pre-filled")
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

// The merged list shows every account's signatures, each acting on its own account; one made there
// goes to the account picked and returns to the merged list.
func TestMergedSignaturesListEveryAccount(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	if r := c.Post(t, "mail.signatures", url.Values{"name": {"Work"}, "text": {"Regards"}}, url.Values{"account": {User}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	r := c.Post(t, "mail.signatures", url.Values{"account": {User2}, "name": {"Home"}, "text": {"Cheers"}})
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != a.Path("mail.signatures") {
		t.Fatalf("a signature made on the merged list: %s to %q", r.Status, r.Header.Get("Location"))
	}
	r = c.Get(t, "mail.signatures")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("the merged list answers %s", r.Status)
	}
	for _, want := range []string{"Regards", "Cheers"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("the merged list lacks %s", want)
		}
	}
	if r := c.Get(t, "mail.signatures", url.Values{"account": {User}}); r.StatusCode != http.StatusOK || strings.Contains(r.Text, "Cheers") {
		t.Fatalf("one account's list: %s, shows another's signature: %v", r.Status, strings.Contains(r.Text, "Cheers"))
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
