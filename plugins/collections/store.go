// Package collections keeps calendars, task lists and address books of
// alborz's own, and serves them by CalDAV and CardDAV; see ADR 24.
package collections

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/record"
)

// ErrNotFound is what a collection, an object or a share that is not
// there answers with.
var ErrNotFound = errors.New("collections: not found")

// Kind is what a collection holds: iCalendar objects or vCards.
type Kind string

const (
	Calendar    Kind = "calendar"
	AddressBook Kind = "addressbook"
)

// Ref names a collection: whose it is, and the name it has in the
// owner's home. Names are the client's to choose when it makes one, so
// they are unique per owner and not across them.
type Ref struct {
	Owner, ID string
}

func (r Ref) key() string { return strings.ToLower(r.Owner) + "/" + r.ID }

// ErrExists is a collection made where one already is.
var ErrExists = errors.New("collections: already exists")

// Collection is one calendar, task list or address book.
type Collection struct {
	ID    string
	Owner string // the account's address
	Kind  Kind
	// Components are what a calendar takes: VEVENT, VTODO or both.
	Components  []string `json:",omitempty"`
	Name        string
	Color       string `json:",omitempty"`
	Description string `json:",omitempty"`
	// CTag changes with every write to the collection or an object in
	// it: what a client asks to learn whether to look again.
	CTag uint64
	// Public, when set, is the secret in the URL that answers the
	// collection to anyone as one stream.
	Public string `json:",omitempty"`
	// Timezone is a calendar's own, the iCalendar a client set it as
	// (RFC 4791 5.2.2).
	Timezone []byte `json:",omitempty"`
	// Dead are the properties clients keep on a collection for
	// themselves - an order, a colour of their own - which alborz hands
	// back as they were sent and never reads (RFC 4918 3).
	Dead []Dead `json:",omitempty"`
}

// Dead is one such property: its name and the element as it was sent.
type Dead struct {
	Space, Local string
	XML          []byte
}

// Object is one event, task or card as it was written, and by whom:
// iCalendar and vCard have no field for who added an object, and in a
// collection several accounts write to that is the thing to know.
type Object struct {
	Name string
	// UID is the identity iCalendar and vCard both give an object (RFC
	// 5545 3.8.4.7, RFC 6350 6.7.6), as its parser read it.
	UID      string `json:",omitempty"`
	Data     []byte
	ETag     string
	Modified time.Time
	// Creator wrote the object first, at Created; Editor wrote it last.
	Creator string    `json:",omitempty"`
	Created time.Time `json:",omitzero"`
	Editor  string    `json:",omitempty"`
}

func (c Collection) Ref() Ref { return Ref{Owner: c.Owner, ID: c.ID} }

// Share is one account's access to another's collection.
type Share struct {
	Owner      string
	Collection string
	To         string
	Write      bool
	// Expires is the last moment of the share; zero never ends.
	Expires  time.Time
	Accepted bool
	Created  time.Time
	// Declined and Left are the invitee's no, before accepting and
	// after: the owner sees which, rather than the share vanishing.
	Declined bool `json:",omitempty"`
	Left     bool `json:",omitempty"`
	// Answered is when the invitee last said yes or no.
	Answered time.Time `json:",omitzero"`
}

func (s Share) Ref() Ref { return Ref{Owner: s.Owner, ID: s.Collection} }

// Live reports whether the share gives access at the moment asked.
func (s Share) Live(now time.Time) bool {
	return s.Accepted && (s.Expires.IsZero() || now.Before(s.Expires))
}

// Ended reports a share past its last day: still the owner's to see
// and remove, and nobody's to use.
func (s Share) Ended() bool {
	return !s.Expires.IsZero() && !time.Now().Before(s.Expires)
}

// Name owns this package's keys in alborz's data file, and Format is
// the format they are written in (ADR 26).
const (
	Name   = "alborz.pim"
	Format = 1
)

func collectionKey(ref Ref) string { return collectionsOf(ref.Owner) + ref.ID }

func collectionsOf(owner string) string { return Name + "/collections/" + strings.ToLower(owner) + "/" }

func objectKey(ref Ref, name string) string { return objectsOf(ref) + name }

func objectsOf(ref Ref) string { return Name + "/objects/" + ref.key() + "/" }

func shareKey(ref Ref, to string) string { return sharesOf(ref) + strings.ToLower(to) }

func sharesOf(ref Ref) string { return Name + "/shares/" + ref.key() + "\x00" }

// uidKey maps a collection's UID to the object holding it, so a write
// learns of a clash without reading every object.
func uidKey(ref Ref, uid string) string { return uidsOf(ref) + uid }

func uidsOf(ref Ref) string { return Name + "/uids/" + ref.key() + "\x00" }

// publicKey maps a published calendar's secret to its collection, so a
// wrong guess reads one key.
func publicKey(secret string) string { return Name + "/public/" + secret }

// receivedKey indexes a share by its recipient.
func receivedKey(to string, ref Ref) string { return receivedBy(to) + ref.key() }

func receivedBy(to string) string { return Name + "/received/" + strings.ToLower(to) + "\x00" }

// ctagKey counts ctags across collections: counted per collection, a
// name deleted and made again would hand out values a client still
// holds from the one before.
const ctagKey = Name + "/ctag"

// Store keeps collections in the data file; every method is one
// transaction.
type Store struct {
	db kv.DB
}

func New(db kv.DB) *Store { return &Store{db: db} }

// publish keeps the index with a collection's secret, which was before.
func publish(tx kv.Tx, before string, c Collection) error {
	if before != "" && before != c.Public {
		if err := tx.Delete(publicKey(before)); err != nil {
			return err
		}
	}
	if c.Public == "" {
		return nil
	}
	return tx.Put(publicKey(c.Public), []byte(collectionKey(c.Ref())))
}

// ETagOf names the bytes, so the same object written twice is the same
// object to a client holding its tag.
func ETagOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func get[T any](tx kv.Tx, key string) (*T, error) {
	raw := tx.Get(key)
	if raw == nil {
		return nil, ErrNotFound
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func each[T any](tx kv.Tx, prefix string, fn func(T) error) error {
	return tx.Scan(prefix, func(_ string, raw []byte) error {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		return fn(v)
	})
}

// put writes v over what is stored under key, keeping what a newer
// version of alborz put there (ADR 26).
func put(tx kv.Tx, key string, v any) error {
	raw, err := record.Keep(tx.Get(key), v)
	if err != nil {
		return err
	}
	return tx.Put(key, raw)
}

// stamp gives a collection a ctag none has had.
func stamp(tx kv.Tx, c *Collection) error {
	var last uint64
	if raw := tx.Get(ctagKey); raw != nil {
		var err error
		if last, err = strconv.ParseUint(string(raw), 10, 64); err != nil {
			return err
		}
	}
	c.CTag = last + 1
	return tx.Put(ctagKey, strconv.AppendUint(nil, c.CTag, 10))
}

// touch moves the collection's ctag on, inside a write's transaction.
func touch(tx kv.Tx, ref Ref) error {
	c, err := get[Collection](tx, collectionKey(ref))
	if err != nil {
		return err
	}
	if err := stamp(tx, c); err != nil {
		return err
	}
	return put(tx, collectionKey(ref), c)
}

// Create keeps a new collection under the name given, or one of its
// own when none is.
func (s *Store) Create(c Collection) (*Collection, error) {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	c.Owner = strings.ToLower(c.Owner)
	err := s.db.Update(func(tx kv.Tx) error {
		if tx.Get(collectionKey(c.Ref())) != nil {
			return ErrExists
		}
		if err := stamp(tx, &c); err != nil {
			return err
		}
		if err := put(tx, collectionKey(c.Ref()), &c); err != nil {
			return err
		}
		return publish(tx, "", c)
	})
	return &c, err
}

func (s *Store) Collection(ref Ref) (*Collection, error) {
	var c *Collection
	err := s.db.View(func(tx kv.Tx) (err error) {
		c, err = get[Collection](tx, collectionKey(ref))
		return err
	})
	return c, err
}

// Update changes a collection's own properties.
func (s *Store) Update(ref Ref, change func(*Collection)) error {
	return s.db.Update(func(tx kv.Tx) error {
		c, err := get[Collection](tx, collectionKey(ref))
		if err != nil {
			return err
		}
		before := c.Public
		change(c)
		if err := stamp(tx, c); err != nil {
			return err
		}
		if err := put(tx, collectionKey(ref), c); err != nil {
			return err
		}
		return publish(tx, before, *c)
	})
}

// Delete removes a collection with its objects and its shares.
func (s *Store) Delete(ref Ref) error {
	return s.db.Update(func(tx kv.Tx) error {
		c, err := get[Collection](tx, collectionKey(ref))
		if err != nil {
			return err
		}
		if err := publish(tx, c.Public, Collection{}); err != nil {
			return err
		}
		var gone []string
		collect := func(k string, _ []byte) error {
			gone = append(gone, k)
			return nil
		}
		for _, prefix := range []string{objectsOf(ref), uidsOf(ref)} {
			if err := tx.Scan(prefix, collect); err != nil {
				return err
			}
		}
		err = each(tx, sharesOf(ref), func(sh Share) error {
			gone = append(gone, shareKey(ref, sh.To), receivedKey(sh.To, ref))
			return nil
		})
		if err != nil {
			return err
		}
		for _, k := range append(gone, collectionKey(ref)) {
			if err := tx.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// Visible are the collections of one kind an account sees: its own, and
// those shared with it that it accepted and that have not run out, each
// with what the account may do with it.
func (s *Store) Visible(account string, kind Kind, now time.Time) ([]Seen, error) {
	account = strings.ToLower(account)
	var out []Seen
	err := s.db.View(func(tx kv.Tx) error {
		err := each(tx, collectionsOf(account), func(c Collection) error {
			if c.Kind == kind {
				out = append(out, Seen{c, own})
			}
			return nil
		})
		if err != nil {
			return err
		}
		prefix := receivedBy(account)
		return tx.Scan(prefix, func(k string, _ []byte) error {
			owner, id, _ := strings.Cut(strings.TrimPrefix(k, prefix), "/")
			ref := Ref{Owner: owner, ID: id}
			sh, err := get[Share](tx, shareKey(ref, account))
			if err != nil || !sh.Live(now) {
				return err
			}
			c, err := get[Collection](tx, collectionKey(ref))
			if err != nil {
				return err
			}
			if c.Kind == kind {
				out = append(out, Seen{*c, accessOf(*sh)})
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

// Seen is a collection and what the account looking may do with it.
type Seen struct {
	Collection
	may access
}

func accessOf(sh Share) access {
	if sh.Write {
		return write
	}
	return read
}

// allowed is what an account may do with a collection, read in the
// transaction that acts on the answer. A collection the account cannot
// see is not found.
func allowed(tx kv.Tx, ref Ref, account string, now time.Time) (*Collection, access, error) {
	c, err := get[Collection](tx, collectionKey(ref))
	if err != nil {
		return nil, none, err
	}
	if c.Owner == account {
		return c, own, nil
	}
	sh, err := get[Share](tx, shareKey(ref, account))
	if err != nil {
		return nil, none, err
	}
	if !sh.Live(now) {
		return nil, none, ErrNotFound
	}
	return c, accessOf(*sh), nil
}

// may is allowed in a transaction of its own.
func (s *Store) may(ref Ref, account string, now time.Time) (c *Collection, may access, err error) {
	err = s.db.View(func(tx kv.Tx) error {
		c, may, err = allowed(tx, ref, account, now)
		return err
	})
	return c, may, err
}

// ErrForbidden is a write by an account the collection is shared with
// to read.
var ErrForbidden = errors.New("collections: not yours to change")

// ByPublic finds the collection a public URL's secret names.
func (s *Store) ByPublic(secret string) (*Collection, error) {
	var found *Collection
	err := s.db.View(func(tx kv.Tx) (err error) {
		key := tx.Get(publicKey(secret))
		if secret == "" || key == nil {
			return ErrNotFound
		}
		found, err = get[Collection](tx, string(key))
		return err
	})
	return found, err
}

func (s *Store) Objects(ref Ref) ([]Object, error) {
	var out []Object
	err := s.db.View(func(tx kv.Tx) error {
		return each(tx, objectsOf(ref), func(o Object) error {
			out = append(out, o)
			return nil
		})
	})
	return out, err
}

func (s *Store) Object(ref Ref, name string) (*Object, error) {
	var o *Object
	err := s.db.View(func(tx kv.Tx) (err error) {
		o, err = get[Object](tx, objectKey(ref, name))
		return err
	})
	return o, err
}

// ErrPrecondition is a write whose If-Match or If-None-Match did not
// hold: somebody else wrote first.
var ErrPrecondition = errors.New("collections: precondition failed")

// ErrUIDConflict is a second object with a UID the collection already
// holds (RFC 4791 5.3.2.1, RFC 6352 6.3.2.1).
var ErrUIDConflict = errors.New("collections: UID already in the collection")

// Unless is a write's condition: given the ETag of the object held,
// empty when none is, it says why the write is off. Nil is a write that
// does not care.
type Unless func(held string) error

// PutObject writes an object as the account by.
func (s *Store) PutObject(ref Ref, name, uid string, data []byte, unless Unless, by string) (*Object, error) {
	now := time.Now().UTC()
	o := &Object{Name: name, UID: uid, Data: data, ETag: ETagOf(data), Modified: now, Creator: by, Created: now, Editor: by}
	err := s.db.Update(func(tx kv.Tx) error {
		if err := writable(tx, ref, by, now); err != nil {
			return err
		}
		old, err := get[Object](tx, objectKey(ref, name))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		held := ""
		if old != nil {
			held = old.ETag
			o.Creator, o.Created = old.Creator, old.Created
		}
		if unless != nil {
			if err := unless(held); err != nil {
				return err
			}
		}
		if uid != "" {
			if holder := tx.Get(uidKey(ref, uid)); holder != nil && string(holder) != name {
				// The index is believed only where the object it names
				// says the same: an entry left behind names nothing.
				other, err := get[Object](tx, objectKey(ref, string(holder)))
				if err != nil && !errors.Is(err, ErrNotFound) {
					return err
				}
				if other != nil && other.UID == uid {
					return ErrUIDConflict
				}
			}
			if err := tx.Put(uidKey(ref, uid), []byte(name)); err != nil {
				return err
			}
		}
		if old != nil && old.UID != "" && old.UID != uid {
			if err := tx.Delete(uidKey(ref, old.UID)); err != nil {
				return err
			}
		}
		if err := put(tx, objectKey(ref, name), o); err != nil {
			return err
		}
		return touch(tx, ref)
	})
	return o, err
}

// writable refuses a write by an account that may not make it.
func writable(tx kv.Tx, ref Ref, by string, now time.Time) error {
	_, may, err := allowed(tx, ref, strings.ToLower(by), now)
	if err == nil && may < write {
		err = ErrForbidden
	}
	return err
}

// RemoveObject deletes an object as the account by, unless its
// condition says not to.
func (s *Store) RemoveObject(ref Ref, name string, unless Unless, by string) error {
	return s.db.Update(func(tx kv.Tx) error {
		if err := writable(tx, ref, by, time.Now()); err != nil {
			return err
		}
		old, err := get[Object](tx, objectKey(ref, name))
		if err != nil {
			return err
		}
		if unless != nil {
			if err := unless(old.ETag); err != nil {
				return err
			}
		}
		if old.UID != "" {
			if err := tx.Delete(uidKey(ref, old.UID)); err != nil {
				return err
			}
		}
		if err := tx.Delete(objectKey(ref, name)); err != nil {
			return err
		}
		return touch(tx, ref)
	})
}

// PutShare keeps a share, replacing one to the same address.
func (s *Store) PutShare(sh Share) error {
	sh.To = strings.ToLower(sh.To)
	return s.db.Update(func(tx kv.Tx) error {
		if tx.Get(collectionKey(sh.Ref())) == nil {
			return ErrNotFound
		}
		if err := put(tx, shareKey(sh.Ref(), sh.To), &sh); err != nil {
			return err
		}
		return tx.Put(receivedKey(sh.To, sh.Ref()), []byte{})
	})
}

func (s *Store) Share(ref Ref, to string) (*Share, error) {
	var sh *Share
	err := s.db.View(func(tx kv.Tx) (err error) {
		sh, err = get[Share](tx, shareKey(ref, to))
		return err
	})
	return sh, err
}

// Answer is the invitee's yes or no to a share: a yes makes it live, a
// no before one is Declined and a no after one is Left.
func (s *Store) Answer(ref Ref, to string, yes bool) error {
	return s.db.Update(func(tx kv.Tx) error {
		sh, err := get[Share](tx, shareKey(ref, to))
		if err != nil {
			return err
		}
		sh.Answered = time.Now().UTC()
		switch {
		case yes:
			sh.Accepted, sh.Declined, sh.Left = true, false, false
		case sh.Accepted:
			sh.Accepted, sh.Left = false, true
		default:
			sh.Declined = true
		}
		return put(tx, shareKey(ref, to), sh)
	})
}

// RemoveShare ends a share: the owner revoking it.
func (s *Store) RemoveShare(ref Ref, to string) error {
	return s.db.Update(func(tx kv.Tx) error {
		if tx.Get(shareKey(ref, to)) == nil {
			return ErrNotFound
		}
		if err := tx.Delete(receivedKey(to, ref)); err != nil {
			return err
		}
		return tx.Delete(shareKey(ref, to))
	})
}

// Shares are every share matching keep: a collection's, or an account's.
func (s *Store) Shares(keep func(Share) bool) ([]Share, error) {
	var out []Share
	err := s.db.View(func(tx kv.Tx) error {
		return each(tx, Name+"/shares/", func(sh Share) error {
			if keep(sh) {
				out = append(out, sh)
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].To < out[j].To })
	return out, err
}
