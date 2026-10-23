package web_test

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/emersion/go-vcard"
)

func copiedBookCards(t *testing.T, c *Client, book, account string) []vcard.Card {
	t.Helper()
	resp := c.Get(t, "contacts.book.export", book, url.Values{"account": {account}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export: %s\n%s", resp.Status, resp.Text)
	}
	dec := vcard.NewDecoder(strings.NewReader(resp.Text))
	var cards []vcard.Card
	for {
		card, err := dec.Decode()
		if err == io.EOF {
			return cards
		}
		if err != nil {
			t.Fatal(err)
		}
		cards = append(cards, card)
	}
}

func TestBulkContactCopyKeepsSelectionAndCompleteCards(t *testing.T) {
	a, c := SignedIn(t, User)
	source, target := c.CreateBook(t, "Source"), c.CreateBook(t, "Target")
	sources := map[string]string{}
	for _, name := range []string{"one", "two", "untouched"} {
		card := strings.Replace(transferCard, "UID:original", "UID:"+name, 1)
		card = strings.Replace(card, "FN:Transfer Contact", "FN:Transfer "+name, 1)
		sources[name] = card
		a.Put(t, source+name+".vcf", "text/vcard", card)
	}
	paths := []string{User + "|" + Here + source + "one.vcf", User + "|" + Here + source + "two.vcf"}
	next := a.Path("contacts.list", url.Values{"q": {"Transfer"}})
	chooser := c.Page(t, "contacts.copy", url.Values{"paths": paths, "next": {next}})
	for _, ref := range paths {
		if !strings.Contains(chooser, `name="paths" value="`+ref+`"`) {
			t.Errorf("chooser did not retain %q", ref)
		}
	}
	// Repeated references must not create a second copy of the same selection.
	resp := c.Post(t, "contacts.copy", url.Values{"paths": {paths[0], paths[1], paths[0]}, "to": {User + "|" + Here + target}, "next": {next}})
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != next {
		t.Fatalf("copy: %s, location %q\n%s", resp.Status, resp.Header.Get("Location"), resp.Text)
	}
	cards := copiedBookCards(t, c, Here+target, User)
	if len(cards) != 2 {
		t.Fatalf("copied %d cards, want 2", len(cards))
	}
	seen := map[string]bool{}
	names := map[string]bool{}
	for _, card := range cards {
		uid := card.Value(vcard.FieldUID)
		if uid == "" || uid == "one" || uid == "two" || seen[uid] {
			t.Errorf("copy did not get a unique UID: %q", uid)
		}
		seen[uid] = true
		if rev := card.Value(vcard.FieldRevision); rev == "" || rev == "20200101T000000Z" {
			t.Errorf("copy revision %q", rev)
		}
		name := strings.TrimPrefix(card.Value(vcard.FieldFormattedName), "Transfer ")
		if (name != "one" && name != "two") || names[name] {
			t.Fatalf("copied an unselected or repeated contact %q", name)
		}
		names[name] = true
		original, err := vcard.NewDecoder(strings.NewReader(sources[name])).Decode()
		if err != nil {
			t.Fatal(err)
		}
		delete(original, vcard.FieldUID)
		delete(original, vcard.FieldRevision)
		delete(card, vcard.FieldUID)
		delete(card, vcard.FieldRevision)
		if !reflect.DeepEqual(card, original) {
			t.Errorf("copy changed card properties: %#v, want %#v", card, original)
		}
	}
	for _, name := range []string{"one", "two", "untouched"} {
		want := sources[name]
		if got := a.DAV(t, http.MethodGet, source+name+".vcf", nil, ""); got.StatusCode != http.StatusOK || got.Text != want {
			t.Errorf("source %s changed: %s", name, got.Text)
		}
	}
}

func TestBulkContactCopySpansSignedInAccounts(t *testing.T) {
	a, c := SignedIn(t, User)
	source := c.CreateBook(t, "People")
	a.Put(t, source+"one.vcf", "text/vcard", transferCard)
	c.SignIn(t, User2)
	for _, name := range []string{"People", "Target"} {
		if resp := c.Post(t, "contacts.books.create", url.Values{"account": {User2}, "place": {"here"}, "name": {name}}); resp.StatusCode != http.StatusFound {
			t.Fatalf("create book: %s", resp.Status)
		}
	}
	source2, target := BooksOf(User2)+"people/", BooksOf(User2)+"target/"
	// Browser authentication selects the second account for this DAV write.
	req, err := http.NewRequest(http.MethodPut, a.URL+source2+"two.vcf", strings.NewReader(strings.Replace(transferCard, "UID:original", "UID:second", 1)))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(User2, Password)
	req.Header.Set("Content-Type", "text/vcard")
	if resp := c.Do(t, req); resp.StatusCode != http.StatusCreated {
		t.Fatalf("second account card: %s\n%s", resp.Status, resp.Text)
	}
	resp := c.Post(t, "contacts.copy", url.Values{"paths": {User + "|" + Here + source + "one.vcf", User2 + "|" + Here + source2 + "two.vcf"}, "to": {User2 + "|" + Here + target}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("cross-account copy: %s\n%s", resp.Status, resp.Text)
	}
	if got := copiedBookCards(t, c, Here+target, User2); len(got) != 2 {
		t.Errorf("destination has %d cards, want 2", len(got))
	}
	if got := copiedBookCards(t, c, Here+source2, User2); len(got) != 1 || got[0].Value(vcard.FieldUID) != "second" {
		t.Error("second account source changed")
	}
}

func TestBulkContactCopyReadonlySourceAndEmptySelection(t *testing.T) {
	a, owner := SignedIn(t, User)
	book := owner.CreateBook(t, "Shared")
	a.Put(t, book+"contact.vcf", "text/vcard", transferCard)
	if resp := owner.Post(t, "contacts.book.share", url.Values{"to": {User2}, "access": {"read"}}, Here+book); resp.StatusCode != http.StatusFound {
		t.Fatalf("share: %s", resp.Status)
	}
	guest := a.SignIn(t, User2)
	shared := Here + BooksOf(User2) + "shared~" + User + "/"
	if resp := guest.Post(t, "contacts.book.accept", nil, shared); resp.StatusCode != http.StatusFound {
		t.Fatalf("accept: %s", resp.Status)
	}
	if resp := guest.Post(t, "contacts.books.create", url.Values{"account": {User2}, "place": {"here"}, "name": {"Target"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("create target: %s", resp.Status)
	}
	target := Here + BooksOf(User2) + "target/"
	paths := []string{User2 + "|" + shared + "contact.vcf"}
	if resp := guest.Post(t, "contacts.copy", url.Values{"paths": paths, "to": {User2 + "|" + shared}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("readonly target: %s", resp.Status)
	}
	if resp := guest.Post(t, "contacts.copy", url.Values{"paths": paths, "to": {User2 + "|" + target}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("readonly source copy: %s\n%s", resp.Status, resp.Text)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		var resp *Response
		if method == http.MethodPost {
			resp = guest.Post(t, "contacts.copy", url.Values{"to": {User2 + "|" + target}})
		} else {
			resp = guest.Get(t, "contacts.copy")
		}
		if resp.StatusCode != http.StatusFound {
			t.Errorf("empty %s: %s", method, resp.Status)
		}
	}
	if got := copiedBookCards(t, guest, target, User2); len(got) != 1 {
		t.Errorf("destination has %d cards after empty selection, want 1", len(got))
	}
	if got := a.DAV(t, http.MethodGet, book+"contact.vcf", nil, ""); got.StatusCode != http.StatusOK || got.Text != transferCard {
		t.Error("readonly source changed")
	}
}

func TestBulkContactCopyRetryKeepsOnlyUnprocessedContacts(t *testing.T) {
	imap := IMAP(t)
	holder := Alborz(t, WithIMAP(imap))
	owner := holder.SignIn(t, User)
	source, target := owner.CreateBook(t, "Source"), owner.CreateBook(t, "Target")
	for _, name := range []string{"one", "two"} {
		holder.Put(t, source+name+".vcf", "text/vcard", strings.Replace(transferCard, "UID:original", "UID:"+name, 1))
	}
	upstream, err := url.Parse(holder.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var puts atomic.Int32
	between := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, target) && puts.Add(1) == 2 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(between.Close)
	a := Alborz(t, WithIMAP(imap), Upstream(Domain, "carddav+insecure://"+strings.TrimPrefix(between.URL, "http://")+holder.Path("dav.collections")))
	c := a.SignIn(t, User)
	paths := []string{User + "|" + At(between.URL) + source + "one.vcf", User + "|" + At(between.URL) + source + "two.vcf"}
	to := User + "|" + At(between.URL) + target
	resp := c.Post(t, "contacts.copy", url.Values{"paths": paths, "to": {to}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("second copy refusal: %s\n%s", resp.Status, resp.Text)
	}
	if !Says(t, resp.Text, "notice.itemscopied", 1) {
		t.Error("partial success did not report the completed copy")
	}
	if got := copiedBookCards(t, owner, Here+target, User); len(got) != 1 {
		t.Fatalf("partial run made %d copies, want 1", len(got))
	}
	var remaining []string
	for _, field := range regexp.MustCompile(`name="paths" value="([^"]*)"`).FindAllStringSubmatch(resp.Text, -1) {
		remaining = append(remaining, html.UnescapeString(field[1]))
	}
	if !reflect.DeepEqual(remaining, paths[1:]) {
		t.Fatalf("retry contains %v, want only %v", remaining, paths[1:])
	}
	if resp := c.Post(t, "contacts.copy", url.Values{"paths": remaining, "to": {to}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("retry: %s\n%s", resp.Status, resp.Text)
	}
	if got := copiedBookCards(t, owner, Here+target, User); len(got) != 2 {
		t.Errorf("retry made %d total copies, want 2", len(got))
	}
	for _, name := range []string{"one", "two"} {
		want := strings.Replace(transferCard, "UID:original", "UID:"+name, 1)
		if got := holder.DAV(t, http.MethodGet, source+name+".vcf", nil, ""); got.StatusCode != http.StatusOK || got.Text != want {
			t.Errorf("source %s changed: %s", name, got.Text)
		}
	}
}
