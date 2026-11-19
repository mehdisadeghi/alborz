package web_test

import (
	"fmt"
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
	a, c := SignedIn(t, User)
	book := c.CreateBook(t, "People")
	a.Put(t, book+"parvin.vcf", "text/vcard",
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:parvin\r\nFN:Parvin Etesami\r\nN:Etesami;Parvin;;;\r\nEND:VCARD\r\n")
	if page := c.Page(t, "contacts.list"); !strings.Contains(page, "Parvin Etesami") {
		t.Errorf("the card in the book on alborz is not on the list")
	}
}

// A search reads every name and address of a card, in any case, on alborz and on a server asked directly.
func TestContactSearchMatchesInAnyCase(t *testing.T) {
	cards := map[string]string{
		"weiss":  "FN:Jürgen Weiß\r\nN:Weiß;Jürgen;;;\r\nORG:Hauptstraße GmbH\r\nEMAIL:jw@example.org\r\nEMAIL:Juergen@Example.net\r\n",
		"ipek":   "FN:İpek Işık\r\nNICKNAME:Pekmez\r\n",
		"maryam": "FN:مریم گزارش\r\n",
	}
	card := func(uid string) string {
		return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\n" + cards[uid] + "END:VCARD\r\n"
	}
	for name, setup := range map[string]func() *Client{
		"on alborz": func() *Client {
			a, c := SignedIn(t, User)
			book := c.CreateBook(t, "People")
			for uid := range cards {
				a.Put(t, book+uid+".vcf", "text/vcard", card(uid))
			}
			return c
		},
		"on a server, asked directly": func() *Client {
			opts := []DAVOption{Book(DAVBooks+"people/", "People")}
			for uid := range cards {
				opts = append(opts, Object(DAVBooks+"people/"+uid+".vcf", card(uid)))
			}
			return Alborz(t, NoCache(), WithDAV(DAV(t, opts...))).SignIn(t, User)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := setup()
			for _, tc := range []struct{ query, found string }{
				{"jürgen", "Jürgen Weiß"}, {"JÜRGEN", "Jürgen Weiß"}, {"weiss", "Jürgen Weiß"}, {"WEIẞ", "Jürgen Weiß"},
				{"hauptstrasse", "Jürgen Weiß"}, {"JW@EXAMPLE.ORG", "Jürgen Weiß"}, {"juergen@example.net", "Jürgen Weiß"},
				{"jürgen -ipek", "Jürgen Weiß"},
				{"ipek", "İpek Işık"}, {"IŞIK", "İpek Işık"}, {"PEKMEZ", "İpek Işık"},
				{"گزارش", "مریم گزارش"},
			} {
				list := Between(html.UnescapeString(c.Page(t, "contacts.list", url.Values{"query": {tc.query}})), "<main", "</main>")
				for uid, text := range cards {
					fn := Between(text, "FN:", "\r\n")
					if got, want := strings.Contains(list, fn), fn == tc.found; got != want {
						t.Errorf("%q: %s (%s) listed %v, want %v", tc.query, fn, uid, got, want)
					}
				}
			}
		})
	}
}

// bookCards are the cards of the account's book on alborz named name, as a DAV client reads them.
func bookCards(t *testing.T, a *App, name string) string {
	t.Helper()
	list := a.DAV(t, "PROPFIND", BooksOf(User), map[string]string{"Depth": "1", "Content-Type": "application/xml"},
		`<propfind xmlns="DAV:"><prop><displayname/></prop></propfind>`).Text
	href := ""
	for r := range strings.SplitSeq(list, "response>") {
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
	a, c := SignedIn(t, User, WithSMTP(SMTP(t)))
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

// The From field can name any signed-in account, so compose offers every account's contacts.
func TestComposeSuggestsEveryAccountsContacts(t *testing.T) {
	_, c := SignedIn(t, User)
	c.SignIn(t, User2)
	if resp := c.Post(t, "contacts.books.create", url.Values{"account": {User2}, "place": {"here"}, "name": {"People"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the second account's book answered %s", resp.Status)
	}
	form := url.Values{"address": {"Parvin Etesami <parvin@example.org>"}, "addressbook": {User2 + "|" + Here + BooksOf(User2) + "people/"}}
	if resp := c.Post(t, "contacts.add", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("adding the contact answered %s", resp.Status)
	}
	datalist := Between(c.Page(t, "mail.compose", url.Values{"account": {User}}), `<datalist id="emails">`, "</datalist>")
	if !strings.Contains(datalist, "parvin@example.org") {
		t.Errorf("compose for %s does not suggest %s's contact:\n%s", User, User2, datalist)
	}
}

func TestAddToContactsWritesTheChosenBook(t *testing.T) {
	a, c := SignedIn(t, User)
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
			for query, books := range map[string][]string{"": {"Friends", failing}, "Ada": {"Friends", failing}} {
				resp := c.Get(t, "contacts.list", url.Values{"query": {query}})
				text := html.UnescapeString(resp.Text)
				if resp.StatusCode != http.StatusOK || !strings.Contains(text, "Ada Lovelace") || strings.Contains(text, "Cut Short") {
					t.Fatalf("the list for %q answered %s, want 200 with Ada alone", query, resp.Status)
				}
				bar := NoticeBar(resp.Text)
				for _, book := range books {
					if !strings.Contains(bar, book) {
						t.Errorf("the list for %q does not name %s as read in part:\n%s", query, book, bar)
					}
				}
			}
			for _, route := range []string{"contacts.contact", "contacts.contact.edit"} {
				if resp := c.Get(t, route, cutCard); resp.StatusCode != http.StatusBadGateway {
					t.Errorf("%s of the cut card answered %s, want 502", route, resp.Status)
				}
			}
		})
	}
}

// contactPage PUTs a VCARD as a DAV client would and opens its edit form.
func contactPage(t *testing.T, a *App, c *Client, book, name, extra string) string {
	t.Helper()
	a.Put(t, book+name+".vcf", "text/vcard",
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:"+name+"\r\nFN:Ada Lovelace\r\n"+extra+"END:VCARD\r\n")
	return c.Page(t, "contacts.contact.edit", Here+book+name+".vcf")
}

// The contact form folds channels and details away: closed on an empty
// create form and when editing a card that fills none of them, auto-opened
// when editing one that fills any.
func TestContactFormMoreOptions(t *testing.T) {
	a, c := SignedIn(t, User)
	book := c.CreateBook(t, "People")

	if page := c.Page(t, "contacts.contact.create"); strings.Contains(page, `<details class="more-options" open`) {
		t.Errorf("an empty create form opens the more-options block")
	}

	for i, open := range []struct{ what, extra string }{
		{"an email", "EMAIL:ada@example.org\r\n"},
		{"a phone", "TEL:+49 151 2345678\r\n"},
		{"an organization", "ORG:Analytical Engines\r\n"},
		{"a job title", "TITLE:Mathematician\r\n"},
		{"a birthday", "BDAY:18121210\r\n"},
		{"a website", "URL:https://example.org/ada\r\n"},
		{"an address", "ADR;TYPE=HOME:;;1 Example Street;London;;12345;UK\r\n"},
		{"a category", "CATEGORIES:friends\r\n"},
		{"a note", "NOTE:First programmer\r\n"},
	} {
		page := contactPage(t, a, c, book, fmt.Sprintf("open-%d", i), open.extra)
		if !strings.Contains(page, `<details class="more-options" open`) {
			t.Errorf("editing a contact with %s does not open the more-options block", open.what)
		}
	}

	page := contactPage(t, a, c, book, "plain", "")
	if strings.Contains(page, `<details class="more-options" open`) {
		t.Errorf("editing a contact with nothing to show still opens the more-options block")
	}
	if !strings.Contains(page, `<details class="more-options">`) {
		t.Errorf("the more-options block is missing from the edit form")
	}
}

func TestHiddenBooksStayOutOfCreatePickers(t *testing.T) {
	_, c := SignedIn(t, User)
	hidden := Here + c.CreateBook(t, "Hidden")
	c.CreateBook(t, "Visible")
	if resp := c.Post(t, "contacts.list", url.Values{"shown": {hidden}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("hide: %s", resp.Status)
	}
	picker := Between(c.Page(t, "contacts.contact.create"), `<select name="addressbook"`, `</select>`)
	if strings.Contains(picker, "Hidden") || !strings.Contains(picker, "Visible") {
		t.Fatalf("picker did not respect visibility: %s", picker)
	}
}
