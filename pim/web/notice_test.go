package web_test

import (
	"net"
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
	if resp := c.Post(t, "contacts.books.create", url.Values{"place": {User + "|here"}, "name": {"Kept"}}); resp.StatusCode != http.StatusFound {
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

// A server whose certificate is not trusted is said with its host and why, as one that did not answer.
func TestAServerNotAnsweringSaysWhy(t *testing.T) {
	untrusted := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(untrusted.Close)
	host := strings.TrimPrefix(untrusted.URL, "https://")
	c := Alborz(t, Upstream(Domain, "caldavs://"+host+"/dav")).SignIn(t, User)
	// The account is a link to its settings.
	at := "\u2066" + Any + "\u2069 (\u2066" + host + "\u2069: " + T(t, "upstream.certificate") + ")"
	if page := c.Page(t, "calendar.month"); !Says(t, page, "notice.unreachable", 1, "CalDAV", at) {
		t.Errorf("the calendar does not say why its server did not answer: %q", NoticeBar(page))
	}
}

// A server that asks to be asked later is said with the time it named.
func TestAThrottlingServerSaysUntilWhen(t *testing.T) {
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(busy.Close)
	host := strings.TrimPrefix(busy.URL, "http://")
	c := Alborz(t, Upstream(Domain, "caldav+insecure://"+host+"/dav")).SignIn(t, User)
	if page := c.Page(t, "calendar.month"); !Says(t, page, "notice.davwait", "CalDAV", User, host, "503 Service Unavailable", Any) {
		t.Errorf("the calendar does not say until when the server asked to wait: %q", NoticeBar(page))
	}
}

// A new book on a server that did not answer is refused naming that server, where it is and why.
func TestANewCollectionOnAServerNotAnsweringSaysWhy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone := ln.Addr().String()
	ln.Close()
	c := Alborz(t, Upstream(Domain, "carddav+insecure://"+gone+"/dav")).SignIn(t, User)
	resp := c.Post(t, "contacts.books.create", url.Values{"place": {User + "|domain"}, "name": {"Kept"}})
	at := "\u2066" + User + "\u2069 (\u2066" + gone + "\u2069: " + T(t, "upstream.refused") + ")"
	if resp.StatusCode != http.StatusUnprocessableEntity || !Says(t, NoticeBar(resp.Text), "upstream.noanswer", T(t, "upstream.dav"), at) {
		t.Errorf("%s: %s", resp.Status, NoticeBar(resp.Text))
	}
}
