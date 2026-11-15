package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// A remote server answering badly is said once per while, naming the service
// and the server's host, and it never replaces what an action said.
func TestAServerAnsweringBadlySaysSoOncePerWhile(t *testing.T) {
	imap := IMAP(t)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	at, err := url.Parse(down.URL)
	if err != nil {
		t.Fatal(err)
	}
	a := Alborz(t, WithIMAP(imap), Upstream(Domain, "carddav+insecure://"+at.Host+"/dav"))
	c := a.SignIn(t, User)

	// What an action said stands: the warning a view came across does not replace it.
	if resp := c.Post(t, "contacts.books.create", url.Values{"account": {User}, "place": {"here"}, "name": {"Kept"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating a book here answered %s", resp.Status)
	}
	after := c.Page(t, "contacts.list")
	if !strings.Contains(NoticeBar(after), "Kept") {
		t.Errorf("the notice of making the book is not the one shown: %q", NoticeBar(after))
	}

	page := c.Page(t, "contacts.list")
	if !Says(t, page, "notice.davanswered", "CardDAV", User, at.Host, Any) {
		t.Fatalf("the list does not say the server answered badly: %q", NoticeBar(page))
	}
	if !strings.Contains(NoticeBar(page), at.Host) {
		t.Errorf("the notice does not name the server's host: %q", NoticeBar(page))
	}
	if again := c.Page(t, "contacts.list"); Says(t, again, "notice.davanswered", "CardDAV", User, at.Host, Any) {
		t.Errorf("the next page repeats the warning: %q", NoticeBar(again))
	}
}
