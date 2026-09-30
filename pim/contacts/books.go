package contacts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	netmail "net/mail"
	"path"
	"strings"
	"uuid"

	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

// Import reads the whole file before writing, so a refusal can say how much of it was saved.
func Import(ctx context.Context, client *carddav.Client, bookPath string, raw []byte) (int, error) {
	dec := vcard.NewDecoder(bytes.NewReader(raw))
	var cards []vcard.Card
	for {
		card, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, &pim.UnreadableError{Err: err}
		}
		cards = append(cards, card)
	}
	put := func(uid string, card vcard.Card) error {
		// What go-vcard will not write is the file's fault, not the server's.
		if err := vcard.NewEncoder(io.Discard).Encode(card); err != nil {
			return &pim.UnreadableError{Err: err}
		}
		_, err := client.PutAddressObject(ctx, path.Join(bookPath, dav.SafeObjectName(uid)+".vcf"), card, nil)
		if err == nil {
			return nil
		}
		existing := cardPathByUID(ctx, client, bookPath, uid)
		if existing == "" {
			return err
		}
		_, err = client.PutAddressObject(ctx, existing, card, nil)
		return err
	}
	n := 0
	var refused []error
	for _, card := range cards {
		if _, ok := card[vcard.FieldVersion]; !ok {
			card.SetValue(vcard.FieldVersion, "4.0")
		}
		uid := card.Value(vcard.FieldUID)
		if uid == "" {
			uid = UIDPrefix + uuid.New().String()
			card.SetValue(vcard.FieldUID, uid)
		}
		err := put(uid, card)
		if pim.ObjectRefused(err) {
			refused = append(refused, fmt.Errorf("%s: %w", uid, err))
			continue
		} else if err != nil {
			return n, &pim.PartlySavedError{Saved: n, Total: len(cards), Err: fmt.Errorf("failed to write %s: %w", uid, err)}
		}
		n++
	}
	return pim.Saved(n, len(cards), refused)
}

func cardPathByUID(ctx context.Context, client *carddav.Client, bookPath, uid string) string {
	objects, err := client.QueryAddressBook(ctx, bookPath, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{Props: []string{vcard.FieldUID}},
		PropFilters: []carddav.PropFilter{{Name: vcard.FieldUID, TextMatches: []carddav.TextMatch{{Text: uid, MatchType: carddav.MatchEquals, Collation: carddav.CollationOctet}}}},
	})
	if err != nil || len(objects) == 0 {
		return ""
	}
	return objects[0].Path
}

func Export(ctx context.Context, client *carddav.Client, bookPath string) ([]byte, error) {
	objects, err := client.QueryAddressBook(ctx, bookPath, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{AllProp: true},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read the address book: %v", err)
	}
	var buf bytes.Buffer
	enc := vcard.NewEncoder(&buf)
	for _, obj := range objects {
		if err := enc.Encode(obj.Card); err != nil {
			return nil, fmt.Errorf("failed to write a card: %v", err)
		}
	}
	return buf.Bytes(), nil
}

// Distinct are the addresses each once, a named form kept over a bare one.
func Distinct(addrs []*netmail.Address) []*netmail.Address {
	var out []*netmail.Address
	at := make(map[string]int)
	for _, a := range addrs {
		// A group's start and end carry no address.
		if !strings.ContainsRune(a.Address, '@') {
			continue
		}
		key := strings.ToLower(a.Address)
		if i, ok := at[key]; ok {
			if out[i].Name == "" {
				out[i].Name = a.Name
			}
			continue
		}
		at[key] = len(out)
		out = append(out, &netmail.Address{Name: a.Name, Address: a.Address})
	}
	return out
}

// Emails are the addresses the books hold, each with its card's name; a book that did not answer is in the error.
func Emails(ctx context.Context, c *carddav.Client, books []dav.Collection) ([]*netmail.Address, error) {
	query := carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{Props: []string{vcard.FieldFormattedName, vcard.FieldEmail}},
		PropFilters: []carddav.PropFilter{{Name: vcard.FieldEmail}},
	}
	// One query per book, run together so the caller waits one round trip.
	results := dav.Each(ctx, books, func(ctx context.Context, book dav.Collection) ([]carddav.AddressObject, error) {
		return c.QueryAddressBook(ctx, book.Path, &query)
	})
	var out []*netmail.Address
	var errs []error
	for _, result := range results {
		errs = append(errs, result.Err)
		for _, ao := range result.Value {
			name := ao.Card.Value(vcard.FieldFormattedName)
			for _, email := range ao.Card.Values(vcard.FieldEmail) {
				if email != "" {
					out = append(out, &netmail.Address{Name: name, Address: email})
				}
			}
		}
	}
	return out, errors.Join(errs...)
}

// AddCard writes a new card for one address into book.
func AddCard(ctx context.Context, c *carddav.Client, book string, addr *netmail.Address) (*carddav.AddressObject, error) {
	id := uuid.New().String()
	name := addr.Name
	if name == "" {
		name = addr.Address
	}
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, UIDPrefix+id)
	card.SetValue(vcard.FieldFormattedName, name)
	card.SetValue(vcard.FieldEmail, addr.Address)
	at, _, ifNoneMatch := pim.Target(book, id+".vcf", "", "")
	ao, err := c.PutAddressObject(ctx, at, card, &carddav.PutAddressObjectOptions{IfNoneMatch: ifNoneMatch})
	if err != nil {
		return nil, err
	}
	ao.Card = card
	return ao, nil
}

// previousKey keeps, per account, what became of its Previous recipients.
const previousKey = "carddav.previous"

type previous struct {
	// Book is the one made for them, empty until the first address; one the reader deleted is not made again.
	Book string
	// Seeded is Sent read for them once.
	Seeded bool
}

func loadPrevious(ctx context.Context, s *alborz.Login) (previous, error) {
	var was previous
	if err := s.Store().Get(ctx, previousKey, &was); err != nil && err != alborz.ErrNoStoreEntry {
		return was, err
	}
	return was, nil
}

// Recent is the point: an old recipient is not who the writer reaches for.
const seedDepth = 200

// Seed fills the book called name with the recipients of the login's recent sent mail, once.
func (p *Books) Seed(ctx context.Context, s *alborz.Login, name string) error {
	was, err := loadPrevious(ctx, s)
	if err == nil && was.Seeded {
		return nil
	}
	var addrs []*netmail.Address
	if err == nil {
		addrs, err = mail.SentRecipients(s, seedDepth)
	}
	if err == nil {
		err = p.Remember(ctx, s, name, addrs, true)
	}
	return err
}

// Remember adds to Previous recipients the addresses no address book of the account holds.
func (p *Books) Remember(ctx context.Context, s *alborz.Login, name string, addrs []*netmail.Address, seeding bool) error {
	p.remembering.Lock()
	defer p.remembering.Unlock()
	was, err := loadPrevious(ctx, s)
	if err != nil {
		return err
	}
	if seeding && was.Seeded {
		return nil
	}
	now := was
	now.Seeded = was.Seeded || seeding
	c, err := p.Client(ctx, s)
	if err != nil {
		return err
	}
	books, err := p.DAV.Collections(ctx, s)
	if err != nil {
		return err
	}
	if now.Book == "" || dav.At(books, now.Book) != nil {
		held, err := Emails(ctx, c, books)
		if err != nil {
			return err
		}
		known := make(map[string]bool)
		for _, a := range held {
			known[strings.ToLower(a.Address)] = true
		}
		var fresh []*netmail.Address
		for _, a := range Distinct(addrs) {
			if !known[strings.ToLower(a.Address)] {
				fresh = append(fresh, a)
			}
		}
		if len(fresh) > 0 && now.Book == "" {
			if now.Book, err = p.DAV.Create(ctx, s, name, dav.DefaultColor, dav.SourceHere, nil); err != nil {
				return err
			}
			if err := s.Store().Put(ctx, previousKey, &now); err != nil {
				return err
			}
		}
		for _, a := range fresh {
			if _, err := AddCard(ctx, c, now.Book, a); err != nil {
				return err
			}
		}
	}
	if now == was {
		return nil
	}
	return s.Store().Put(ctx, previousKey, &now)
}
