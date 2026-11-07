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

const transferCard = "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:original\r\nREV:20200101T000000Z\r\nFN:Transfer Contact\r\nEMAIL;TYPE=work:one@example.org\r\nEMAIL;TYPE=home:two@example.org\r\nPHOTO:data:image/png;base64,aGVsbG8=\r\nX-CUSTOM:kept\r\nEND:VCARD\r\n"

func TestContactTransfersKeepTheCompleteCard(t *testing.T) {
	for _, move := range []bool{false, true} {
		t.Run(map[bool]string{false: "copy", true: "move"}[move], func(t *testing.T) {
			a, c := SignedIn(t, User)
			source, target := c.CreateBook(t, "Source"), c.CreateBook(t, "Target")
			a.Put(t, source+"contact.vcf", "text/vcard", transferCard)
			route := "contacts.contact.copy"
			if move {
				route = "contacts.contact.move"
			}
			resp := c.Post(t, route, url.Values{"to": {User + "|" + Here + target}}, Here+source+"contact.vcf")
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("transfer: %s\n%s", resp.Status, resp.Text)
			}
			u, err := url.Parse(resp.Header.Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			_, params, ok := a.Server.Match(u)
			if !ok {
				t.Fatal("transfer destination has no route")
			}
			body := a.DAV(t, http.MethodGet, strings.TrimPrefix(params["path"], Here), nil, "").Text
			for _, kept := range []string{"one@example.org", "two@example.org", "aGVsbG8=", "X-CUSTOM:kept"} {
				if !strings.Contains(body, kept) {
					t.Errorf("lost %s: %s", kept, body)
				}
			}
			if strings.Contains(body, "UID:original") != move || strings.Contains(body, "REV:20200101T000000Z") != move {
				t.Errorf("wrong identity/revision: %s", body)
			}
			want := http.StatusOK
			if move {
				want = http.StatusNotFound
			}
			if got := a.DAV(t, http.MethodGet, source+"contact.vcf", nil, "").StatusCode; got != want {
				t.Errorf("source status %d, want %d", got, want)
			}
		})
	}
}

func TestContactMoveCollisionPreservesBothCards(t *testing.T) {
	a, c := SignedIn(t, User)
	source, target := c.CreateBook(t, "Source"), c.CreateBook(t, "Target")
	a.Put(t, source+"contact.vcf", "text/vcard", transferCard)
	existing := strings.Replace(transferCard, "UID:original", "UID:destination", 1)
	a.Put(t, target+"contact.vcf", "text/vcard", existing)
	resp := c.Post(t, "contacts.contact.move", url.Values{"to": {User + "|" + Here + target}}, Here+source+"contact.vcf")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("collision: %s", resp.Status)
	}
	if got := a.DAV(t, http.MethodGet, source+"contact.vcf", nil, "").Text; got != transferCard {
		t.Errorf("source changed: %s", got)
	}
	if got := a.DAV(t, http.MethodGet, target+"contact.vcf", nil, "").Text; got != existing {
		t.Errorf("destination overwritten: %s", got)
	}
}

func TestContactTransferAcrossSignedInAccounts(t *testing.T) {
	a, c := SignedIn(t, User)
	source := c.CreateBook(t, "People")
	c.SignIn(t, User2)
	if resp := c.Post(t, "contacts.books.create", url.Values{"account": {User2}, "place": {"here"}, "name": {"People"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("create second account book: %s", resp.Status)
	}
	target := BooksOf(User2) + "people/"
	a.Put(t, source+"contact.vcf", "text/vcard", transferCard)
	resp := c.Post(t, "contacts.contact.move", url.Values{"to": {User2 + "|" + Here + target}}, Here+source+"contact.vcf", url.Values{"account": {User}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("cross-account transfer: %s\n%s", resp.Status, resp.Text)
	}
	landed, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if landed.Query().Get("account") != User2 {
		t.Errorf("destination account absent: %s", resp.Header.Get("Location"))
	}
	if c.Get(t, "contacts.contact", Here+target+"contact.vcf", url.Values{"account": {User2}}).StatusCode != http.StatusOK {
		t.Fatal("destination missing")
	}
}

func TestReadonlyContactCopiesButCannotMove(t *testing.T) {
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
	form := url.Values{"to": {User2 + "|" + target}}
	if resp := guest.Post(t, "contacts.contact.move", form, shared+"contact.vcf"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("readonly move: %s", resp.Status)
	}
	if resp := guest.Post(t, "contacts.contact.copy", form, shared+"contact.vcf"); resp.StatusCode != http.StatusFound {
		t.Fatalf("readonly copy: %s\n%s", resp.Status, resp.Text)
	}
	if resp := guest.Post(t, "contacts.contact.copy", url.Values{"to": {User2 + "|" + shared}}, shared+"contact.vcf"); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("readonly destination accepted: %s", resp.Status)
	}
	guest.SignIn(t, User)
	across := url.Values{"to": {User + "|" + Here + book}}
	if resp := guest.Post(t, "contacts.contact.move", across, shared+"contact.vcf", url.Values{"account": {User2}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-account readonly move: %s", resp.Status)
	}
	if resp := guest.Post(t, "contacts.contact.copy", across, shared+"contact.vcf", url.Values{"account": {User2}}); resp.StatusCode != http.StatusFound {
		t.Errorf("cross-account readonly copy: %s", resp.Status)
	}
	if got := a.DAV(t, http.MethodGet, book+"contact.vcf", nil, ""); got.StatusCode != http.StatusOK {
		t.Fatal("readonly source lost")
	}
}

func TestContactMoveDestinationRefusalPreservesSource(t *testing.T) {
	source, target := DAVBooks+"source/", DAVBooks+"target/"
	d := DAV(t, Book(source, "Source"), Book(target, "Target"), Object(source+"contact.vcf", transferCard), Refusing(target))
	c := Alborz(t, WithDAV(d)).SignIn(t, User)
	resp := c.Post(t, "contacts.contact.move", url.Values{"to": {User + "|" + d.At() + target}}, d.At()+source+"contact.vcf")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("refused destination: %s", resp.Status)
	}
	if got := c.Page(t, "contacts.contact.raw", d.At()+source+"contact.vcf"); got != transferCard {
		t.Errorf("source changed: %s", got)
	}
}

func TestContactMoveDeletionRefusalRollsBackDestination(t *testing.T) {
	imap := IMAP(t)
	holder := Alborz(t, WithIMAP(imap))
	owner := holder.SignIn(t, User)
	source, target := owner.CreateBook(t, "Source"), owner.CreateBook(t, "Target")
	holder.Put(t, source+"contact.vcf", "text/vcard", transferCard)
	upstream, err := url.Parse(holder.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(upstream) }}
	between := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "MOVE" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == source+"contact.vcf" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(between.Close)
	a := Alborz(t, WithIMAP(imap), Upstream(Domain, "carddav+insecure://"+strings.TrimPrefix(between.URL, "http://")+holder.Path("dav.collections")))
	c := a.SignIn(t, User)
	resp := c.Post(t, "contacts.contact.move", url.Values{"to": {User + "|" + At(between.URL) + target}}, At(between.URL)+source+"contact.vcf")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("refused delete: %s", resp.Status)
	}
	if got := holder.DAV(t, http.MethodGet, source+"contact.vcf", nil, ""); got.StatusCode != http.StatusOK || got.Text != transferCard {
		t.Errorf("source changed: %s", got.Text)
	}
	if got := holder.DAV(t, http.MethodGet, target+"contact.vcf", nil, ""); got.StatusCode != http.StatusNotFound {
		t.Errorf("destination not rolled back: %s", got.Status)
	}
}

func TestContactMoveUncertainDeletionKeepsDestination(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, 0, http.StatusPreconditionFailed, http.StatusNotFound} {
		name := http.StatusText(status)
		if status == 0 {
			name = "lost response"
		}
		t.Run(name, func(t *testing.T) {
			imap := IMAP(t)
			holder := Alborz(t, WithIMAP(imap))
			owner := holder.SignIn(t, User)
			source, target := owner.CreateBook(t, "Source"), owner.CreateBook(t, "Target")
			holder.Put(t, source+"contact.vcf", "text/vcard", transferCard)
			upstream, err := url.Parse(holder.URL)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			between := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "MOVE" {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if r.Method == http.MethodDelete && r.URL.Path == source+"contact.vcf" {
					deleted := holder.DAV(t, http.MethodDelete, r.URL.Path, map[string]string{"If-Match": r.Header.Get("If-Match")}, "")
					if deleted.StatusCode != http.StatusNoContent {
						t.Errorf("upstream delete: %s", deleted.Status)
					}
					if status == 0 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							conn.Close()
						}
						return
					}
					w.WriteHeader(status)
					return
				}
				proxy.ServeHTTP(w, r)
			}))
			t.Cleanup(between.Close)
			a := Alborz(t, WithIMAP(imap), Upstream(Domain, "carddav+insecure://"+strings.TrimPrefix(between.URL, "http://")+holder.Path("dav.collections")))
			c := a.SignIn(t, User)
			resp := c.Post(t, "contacts.contact.move", url.Values{"to": {User + "|" + At(between.URL) + target}}, At(between.URL)+source+"contact.vcf")
			if got := holder.DAV(t, http.MethodGet, source+"contact.vcf", nil, ""); got.StatusCode != http.StatusNotFound {
				t.Errorf("source delete was not applied: %s", got.Status)
			}
			if got := holder.DAV(t, http.MethodGet, target+"contact.vcf", nil, ""); got.StatusCode != http.StatusOK || !strings.Contains(got.Text, "UID:original") {
				t.Errorf("last copy lost: %s", got.Status)
			}
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("uncertain delete: %s", resp.Status)
			}
		})
	}
}

func TestContactMoveWithoutStrongSourceVersionWritesNothing(t *testing.T) {
	for _, etag := range []string{"", `W/"weak"`} {
		t.Run(etag, func(t *testing.T) {
			source, target := DAVBooks+"source/", DAVBooks+"target/"
			d := DAV(t, Book(source, "Source"), Book(target, "Target"), Object(source+"contact.vcf", transferCard))
			upstream, err := url.Parse(d.URL)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			proxy.ModifyResponse = func(resp *http.Response) error {
				if resp.Request.Method == http.MethodGet && resp.Request.URL.Path == source+"contact.vcf" {
					resp.Header.Set("Content-Type", "text/vcard")
					resp.Header.Set("ETag", etag)
				}
				return nil
			}
			between := httptest.NewServer(proxy)
			t.Cleanup(between.Close)
			a := Alborz(t, NoCache(), Upstream(Domain, "carddav+insecure://"+strings.TrimPrefix(between.URL, "http://")+"/dav"))
			c := a.SignIn(t, User)
			c.SignIn(t, User2)
			resp := c.Post(t, "contacts.contact.move", url.Values{"to": {User2 + "|" + At(between.URL) + target}}, At(between.URL)+source+"contact.vcf", url.Values{"account": {User}})
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("unversioned source: %s", resp.Status)
			}
			if len(d.Puts()) != 0 {
				t.Error("unversioned source caused a destination write")
			}
		})
	}
}

func TestContactMoveWithoutStrongDestinationVersionKeepsSource(t *testing.T) {
	for _, etag := range []string{"", `W/"weak"`} {
		t.Run(etag, func(t *testing.T) {
			source, target := DAVBooks+"source/", DAVBooks+"target/"
			d := DAV(t, Book(source, "Source"), Book(target, "Target"), Object(source+"contact.vcf", transferCard))
			upstream, err := url.Parse(d.URL)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			proxy.ModifyResponse = func(resp *http.Response) error {
				if resp.Request.Method == http.MethodGet {
					resp.Header.Set("Content-Type", "text/vcard")
				}
				if resp.Request.Method == http.MethodPut {
					resp.Header.Set("ETag", etag)
				}
				return nil
			}
			var deletes atomic.Int32
			between := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deletes.Add(1)
				}
				proxy.ServeHTTP(w, r)
			}))
			t.Cleanup(between.Close)
			a := Alborz(t, NoCache(), Upstream(Domain, "carddav+insecure://"+strings.TrimPrefix(between.URL, "http://")+"/dav"))
			c := a.SignIn(t, User)
			c.SignIn(t, User2)
			resp := c.Post(t, "contacts.contact.move", url.Values{"to": {User2 + "|" + At(between.URL) + target}}, At(between.URL)+source+"contact.vcf", url.Values{"account": {User}})
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("unversioned destination: %s", resp.Status)
			}
			if deletes.Load() != 0 {
				t.Error("unversioned destination caused deletion")
			}
		})
	}
}

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
