package store

import (
	"bytes"
	"context"
	"errors"
	"strconv"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

type books struct{ *Store }

func (b books) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return principalPath(userOf(ctx)), nil
}

func (b books) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return HomePath(AddressBook, userOf(ctx)), nil
}

func bookOf(viewer string, c Collection, may access) (carddav.AddressBook, error) {
	return carddav.AddressBook{
		Path: PathOf(viewer, c), Name: c.Name, Description: c.Description,
		ReadOnly: may < write, CTag: strconv.FormatUint(c.CTag, 10), DeadProperties: deadOf(c),
	}, nil
}

func (b books) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	return visible(ctx, b.Store, AddressBook, bookOf)
}

func (b books) GetAddressBook(ctx context.Context, p string) (*carddav.AddressBook, error) {
	return visibleOne(ctx, b.Store, p, AddressBook, bookOf)
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

func addressObject(p string, o Object, req *carddav.AddressDataRequest) carddav.AddressObject {
	ao := carddav.AddressObject{Path: p, ModTime: o.Modified, ContentLength: int64(len(o.Data)), ETag: o.ETag}
	if !req.IsEmpty() {
		ao.Raw = o.Data
	}
	return ao
}

func (b books) GetAddressObject(ctx context.Context, p string, req *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	return objectAs(ctx, b.Store, p, AddressBook, func(p string, o Object) carddav.AddressObject { return addressObject(p, o, req) })
}

func (b books) ListAddressObjects(ctx context.Context, p string, req *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	return objectsAs(ctx, b.Store, p, AddressBook, func(p string, o Object) carddav.AddressObject { return addressObject(p, o, req) })
}

// QueryAddressObjects parses only when the query has a filter to run. A card
// that does not parse goes to the filter as its bytes, which it keeps for the
// client to judge.
func (b books) QueryAddressObjects(ctx context.Context, p string, query *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	objects, err := b.objects(ctx, p, AddressBook)
	if err != nil {
		return nil, err
	}
	all := queryObjects(objects, p, func(path string, o Object) carddav.AddressObject {
		return addressObject(path, o, &query.DataRequest)
	}, func(object *carddav.AddressObject, raw []byte) {
		if len(query.PropFilters) == 0 {
			return
		}
		object.Card, err = vcard.NewDecoder(bytes.NewReader(raw)).Decode()
		if err != nil {
			object.Card, object.Raw = nil, raw
		}
	})
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
