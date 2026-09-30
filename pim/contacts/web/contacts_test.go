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

	cards := bookCards(t, a, "Previous recipients")
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
