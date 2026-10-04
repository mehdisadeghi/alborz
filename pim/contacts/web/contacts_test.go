package web_test

import (
	"html"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// An address book query with no filter asks for every card (RFC 6352 10.5).
func TestABookOnAlborzListsItsCards(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	book := c.CreateBook(t, "People")
	a.Put(t, book+"parvin.vcf", "text/vcard",
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:parvin\r\nFN:Parvin Etesami\r\nN:Etesami;Parvin;;;\r\nEND:VCARD\r\n")
	if page := c.Page(t, "contacts.list"); !strings.Contains(page, "Parvin Etesami") {
		t.Errorf("the card in the book on alborz is not on the list")
	}
}

// bookCards are the cards of the account's book on alborz named name, as a DAV client reads them.
func bookCards(t *testing.T, a *App, name string) string {
	t.Helper()
	list := a.DAV(t, "PROPFIND", BooksOf(User), map[string]string{"Depth": "1", "Content-Type": "application/xml"},
		`<propfind xmlns="DAV:"><prop><displayname/></prop></propfind>`).Text
	href := ""
	for _, r := range strings.Split(list, "response>") {
		if strings.Contains(r, ">"+name+"<") {
			href = Between(r, "href>", "<")
		}
	}
	if href == "" {
		t.Fatalf("no book named %q:\n%s", name, list)
	}
	return a.DAV(t, "REPORT", href, map[string]string{"Depth": "1", "Content-Type": "application/xml"},
		`<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><C:address-data/></D:prop></C:addressbook-query>`).Text
}

func TestSendingKeepsNewRecipientsInPreviousRecipients(t *testing.T) {
	a := Alborz(t, WithSMTP(SMTP(t)))
	c := a.SignIn(t, User)
	book := c.CreateBook(t, "People")
	a.Put(t, book+"known.vcf", "text/vcard",
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:known\r\nFN:Known\r\nEMAIL:known@example.org\r\nEND:VCARD\r\n")

	c.Send(t, c.Page(t, "mail.compose"),
		url.Values{"to": {"Parvin Etesami <parvin@example.org>"}, "cc": {"known@example.org"}}, "mail.compose")

	cards := bookCards(t, a, T(t, "contacts.previous"))
	if !strings.Contains(cards, "EMAIL:parvin@example.org") || !strings.Contains(cards, "FN:Parvin Etesami") {
		t.Errorf("the new recipient is not in Previous recipients:\n%s", cards)
	}
	if strings.Contains(cards, "known@example.org") {
		t.Errorf("a recipient another book holds was kept again:\n%s", cards)
	}
	if !strings.Contains(c.Page(t, "mail.compose"), "parvin@example.org") {
		t.Errorf("compose does not suggest the previous recipient")
	}
}

func TestAddToContactsWritesTheChosenBook(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.CreateBook(t, "People")
	c.CreateBook(t, "Work")

	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	dialog := Between(c.Page(t, "mail.message", "INBOX", uids[0]), `<dialog id="add-contact"`, "</dialog>")
	address := html.UnescapeString(Between(dialog, `<option value="`, `"`))
	book := ""
	for _, v := range strings.Split(dialog, `<option value="`)[1:] {
		if v = html.UnescapeString(v[:strings.IndexByte(v, '"')]); strings.HasSuffix(v, "/work/") {
			book = v
		}
	}
	if address == "" || book == "" {
		t.Fatalf("the message page offers no address or no Work book:\n%s", dialog)
	}

	form := url.Values{"address": {address}, "addressbook": {book}, "next": {a.Path("mail.message", "INBOX", uids[0])}}
	for range 2 {
		if resp := c.Post(t, "contacts.add", form); resp.StatusCode != http.StatusFound {
			t.Fatalf("adding the address answered %s", resp.Status)
		}
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	if cards := bookCards(t, a, "Work"); strings.Count(cards, "EMAIL:"+parsed.Address) != 1 {
		t.Errorf("Work does not hold %s once:\n%s", parsed.Address, cards)
	}
	if cards := bookCards(t, a, "People"); strings.Contains(cards, parsed.Address) {
		t.Errorf("a book not chosen got the card:\n%s", cards)
	}
}

// A copy kept while alborz was down may be any age behind its server: refilled there under a new
// token, as a seed recreates a book, it shows the server's cards on its first read, not the old ones.
func TestABookRefilledWhileAlborzWasDownShowsItsNewCards(t *testing.T) {
	book := DAVBooks + "contacts/"
	card := func(uid, name string) string {
		return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + name + "\r\nEND:VCARD\r\n"
	}
	d := DAV(t, Book(book, "Contacts"),
		Object(book+"ada.vcf", card("ada", "Ada Lovelace")), Object(book+"emmy.vcf", card("emmy", "Emmy Noether")))
	data, cache := DataDir(t.TempDir()), CacheDir(t.TempDir())

	a := Alborz(t, WithDAV(d), data, cache)
	page := a.SignIn(t, User).Page(t, "contacts.list")
	if !strings.Contains(page, "Ada Lovelace") || !strings.Contains(page, "Emmy Noether") {
		t.Fatalf("the list does not show both cards before the book is refilled")
	}
	a.Close()

	d.Apply(Removed(book+"ada.vcf", book+"emmy.vcf"), Object(book+"parvin.vcf", card("parvin", "Parvin Etesami")), SyncToken("2"))
	page = Alborz(t, WithDAV(d), data, cache).SignIn(t, User).Page(t, "contacts.list")
	if !strings.Contains(page, "Parvin Etesami") {
		t.Errorf("the list does not show the card the server holds now")
	}
	if strings.Contains(page, "Ada Lovelace") || strings.Contains(page, "Emmy Noether") {
		t.Errorf("the list still shows cards the server no longer holds")
	}
}

// A card alborz cannot read costs the page only itself and a notice naming its book, and a book its
// server cannot serve costs only itself; the card's own page says it cannot be read, and nothing is
// written over it.
func TestABookWithAnUnreadableCardStillShowsTheRest(t *testing.T) {
	ada := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:ada\r\nFN:Ada Lovelace\r\nEND:VCARD\r\n"
	cut := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:cut\r\nFN:Cut Short\r\n"
	friends, family := DAVBooks+"friends/", DAVBooks+"family/"
	served := func(opts ...Option) (*Client, string, string) {
		d := DAV(t, Book(friends, "Friends"), Book(family, "Family"), Failing(family),
			Object(friends+"ada.vcf", ada), Object(friends+"cut.vcf", cut))
		return Alborz(t, append(opts, WithDAV(d))...).SignIn(t, User), d.At() + friends + "cut.vcf", "Family"
	}
	for _, tc := range []struct {
		name  string
		setup func() (*Client, string, string)
	}{
		{"on a server, through the mirror", func() (*Client, string, string) { return served() }},
		{"on a server, asked directly", func() (*Client, string, string) { return served(NoCache()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, cutCard, failing := tc.setup()
			// A search the server runs leaves the cut card out itself.
			for query, books := range map[string][]string{"": {"Friends", failing}, "Ada": {failing}} {
				resp := c.Get(t, "contacts.list", url.Values{"query": {query}})
				text := html.UnescapeString(resp.Text)
				if resp.StatusCode != http.StatusOK || !strings.Contains(text, "Ada Lovelace") || strings.Contains(text, "Cut Short") {
					t.Fatalf("the list for %q answered %s, want 200 with Ada alone", query, resp.Status)
				}
				bar := NoticeBar(resp.Text)
				for _, book := range books {
					if !strings.Contains(bar, book) || !Says(t, bar, "notice.unread", len(books), Any) {
						t.Errorf("the list for %q does not name %s as read in part:\n%s", query, book, bar)
					}
				}
			}
			for _, route := range []string{"contacts.contact", "contacts.contact.edit"} {
				if resp := c.Get(t, route, cutCard); resp.StatusCode != http.StatusBadGateway || !Says(t, resp.Text, "unreadable") {
					t.Errorf("%s of the cut card answered %s, want 502 saying it cannot be read", route, resp.Status)
				}
			}
		})
	}
}
