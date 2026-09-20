package collections

import (
	"bytes"
	"context"
	"errors"
	"path"
	"strconv"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

// books is the store as go-webdav's CardDAV server asks for it.
type books struct{ *Store }

func (b books) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return principalPath(userOf(ctx)), nil
}

func (b books) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return HomePath(AddressBook, userOf(ctx)), nil
}

func bookOf(viewer string, c Collection, may access) carddav.AddressBook {
	return carddav.AddressBook{
		Path: PathOf(viewer, c), Name: c.Name, Description: c.Description,
		ReadOnly: may < write, CTag: strconv.FormatUint(c.CTag, 10), DeadProperties: deadOf(c),
	}
}

func (b books) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	visible, err := b.Visible(userOf(ctx), AddressBook, time.Now())
	if err != nil {
		return nil, err
	}
	out := make([]carddav.AddressBook, len(visible))
	for i, c := range visible {
		out[i] = bookOf(userOf(ctx), c.Collection, c.may)
	}
	return out, nil
}

func (b books) GetAddressBook(ctx context.Context, p string) (*carddav.AddressBook, error) {
	_, c, may, err := b.reach(ctx, p, AddressBook)
	if err != nil {
		return nil, err
	}
	book := bookOf(userOf(ctx), *c, may)
	return &book, nil
}

func (b books) CreateAddressBook(ctx context.Context, book *carddav.AddressBook) error {
	c := Collection{Kind: AddressBook, Name: book.Name, Description: book.Description}
	keepDead(&c, nil, book.DeadProperties)
	return b.create(ctx, book.Path, c)
}

func (b books) UpdateAddressBook(ctx context.Context, p string, update *carddav.AddressBookUpdate) error {
	return b.update(ctx, p, AddressBook, func(c *Collection) {
		set(&c.Name, update.Name)
		set(&c.Description, update.Description)
		keepDead(c, update.RemovedDeadProperties, update.DeadProperties)
	})
}

func (b books) DeleteAddressBook(ctx context.Context, p string) error {
	return b.remove(ctx, p, AddressBook, "")
}

// addressObject is calendarObject for a card.
func addressObject(p string, o Object, req *carddav.AddressDataRequest) carddav.AddressObject {
	ao := carddav.AddressObject{Path: p, ModTime: o.Modified, ContentLength: int64(len(o.Data)), ETag: o.ETag}
	if !req.IsEmpty() {
		ao.Raw = o.Data
	}
	return ao
}

func (b books) GetAddressObject(ctx context.Context, p string, req *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	o, err := b.object(ctx, p, AddressBook)
	if err != nil {
		return nil, err
	}
	ao := addressObject(p, *o, req)
	return &ao, nil
}

func (b books) ListAddressObjects(ctx context.Context, p string, req *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	objects, err := b.objects(ctx, p, AddressBook)
	if err != nil {
		return nil, err
	}
	out := make([]carddav.AddressObject, len(objects))
	for i, o := range objects {
		out[i] = addressObject(path.Join(p, o.Name), o, req)
	}
	return out, nil
}

// QueryAddressObjects parses where the query has a filter to run; the
// whole book, which is what alborz's own list asks for, parses nothing.
func (b books) QueryAddressObjects(ctx context.Context, p string, query *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	objects, err := b.objects(ctx, p, AddressBook)
	if err != nil {
		return nil, err
	}
	all := make([]carddav.AddressObject, len(objects))
	for i, o := range objects {
		all[i] = addressObject(path.Join(p, o.Name), o, &query.DataRequest)
		if len(query.PropFilters) == 0 {
			continue
		}
		if all[i].Card, err = vcard.NewDecoder(bytes.NewReader(o.Data)).Decode(); err != nil {
			return nil, err
		}
	}
	return carddav.Filter(query, all)
}

func (b books) PutAddressObject(ctx context.Context, p string, card vcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	o, err := b.write(ctx, p, AddressBook, card.Value(vcard.FieldUID), opts.Raw, opts.IfMatch, opts.IfNoneMatch,
		func(Collection) error { return nil })
	if errors.Is(err, ErrUIDConflict) {
		return nil, carddav.NewPreconditionError(carddav.PreconditionNoUIDConflict)
	}
	if err != nil {
		return nil, err
	}
	return &carddav.AddressObject{Path: p, ModTime: o.Modified, ContentLength: int64(len(o.Data)), ETag: o.ETag}, nil
}

func (b books) DeleteAddressObject(ctx context.Context, p string) error {
	return b.remove(ctx, p, AddressBook, "")
}

func (b books) DeleteAddressObjectIfMatch(ctx context.Context, p string, ifMatch webdav.ConditionalMatch) error {
	return b.remove(ctx, p, AddressBook, ifMatch)
}
