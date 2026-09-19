// Package collections keeps calendars, task lists and address books of
// alborz's own, and serves them by CalDAV and CardDAV; see ADR 24.
package collections

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// ErrNotFound is what a collection, an object or a share that is not
// there answers with.
var ErrNotFound = errors.New("collections: not found")

// fileName is the store's file in the data directory.
const fileName = "collections.db"

// openTimeout is how long a second alborz on the same data directory
// waits for the file before saying what is wrong, as the visits do.
const openTimeout = 5 * time.Second

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

// Object is one event, task or card as it was written.
type Object struct {
	Name     string
	Data     []byte
	ETag     string
	Modified time.Time
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

var (
	collectionsBucket = []byte("collections")
	objectsBucket     = []byte("objects")
	sharesBucket      = []byte("shares")
	// publicBucket maps a published calendar's secret to its collection:
	// a feed is asked for by anyone, and a wrong guess reads one key
	// rather than every collection of every account.
	publicBucket = []byte("public")
)

// Store is the file and nothing else; every method is one transaction.
type Store struct {
	db *bolt.DB
}

// Open opens the store in dir, making the file if it is not there. The
// directory is the operator's to make, as it is for the visits.
func Open(dir string) (*Store, error) {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not there: it holds the calendars and address books kept here; "+
			"make it, or name another with -data-dir", dir)
	}
	path := filepath.Join(dir, fileName)
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: openTimeout})
	if err != nil {
		if errors.Is(err, bolt.ErrTimeout) {
			return nil, fmt.Errorf("%s is locked: another alborz is running on this data directory (waited %v)", path, openTimeout)
		}
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		unpublished := tx.Bucket(publicBucket) == nil
		for _, name := range [][]byte{collectionsBucket, objectsBucket, sharesBucket, publicBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		if unpublished {
			return indexPublic(tx)
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// indexPublic fills the index from the calendars published before it
// was kept.
func indexPublic(tx *bolt.Tx) error {
	return each(tx.Bucket(collectionsBucket), nil, func(c Collection) error {
		return publish(tx, "", c)
	})
}

// publish keeps the index with a collection's secret, which was before.
func publish(tx *bolt.Tx, before string, c Collection) error {
	index := tx.Bucket(publicBucket)
	if before != "" && before != c.Public {
		if err := index.Delete([]byte(before)); err != nil {
			return err
		}
	}
	if c.Public == "" {
		return nil
	}
	return index.Put([]byte(c.Public), []byte(c.Ref().key()))
}

// ETagOf names the bytes, so the same object written twice is the same
// object to a client holding its tag.
func ETagOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func objectKey(ref Ref, name string) []byte { return []byte(ref.key() + "/" + name) }

func shareKey(ref Ref, to string) []byte { return []byte(ref.key() + "\x00" + strings.ToLower(to)) }

func get[T any](b *bolt.Bucket, key []byte) (*T, error) {
	raw := b.Get(key)
	if raw == nil {
		return nil, ErrNotFound
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// each reads every record of b whose key begins with prefix.
func each[T any](b *bolt.Bucket, prefix []byte, fn func(T) error) error {
	cur := b.Cursor()
	for k, raw := cur.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, raw = cur.Next() {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		if err := fn(v); err != nil {
			return err
		}
	}
	return nil
}

func put(b *bolt.Bucket, key []byte, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Put(key, raw)
}

// stamp gives a collection a ctag none has had, from the bucket's own
// sequence: counted per collection, a name deleted and made again went
// back through the values a client still held from the one before.
func stamp(b *bolt.Bucket, c *Collection) error {
	next, err := b.NextSequence()
	if err != nil {
		return err
	}
	// A ctag counted before the sequence was kept may be ahead of it.
	if next <= c.CTag {
		next = c.CTag + 1
		if err := b.SetSequence(next); err != nil {
			return err
		}
	}
	c.CTag = next
	return nil
}

// touch moves the collection's ctag on, inside a write's transaction.
func touch(tx *bolt.Tx, ref Ref) error {
	b := tx.Bucket(collectionsBucket)
	c, err := get[Collection](b, []byte(ref.key()))
	if err != nil {
		return err
	}
	if err := stamp(b, c); err != nil {
		return err
	}
	return put(b, []byte(ref.key()), c)
}

// Create keeps a new collection under the name given, or one of its
// own when none is.
func (s *Store) Create(c Collection) (*Collection, error) {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	c.Owner = strings.ToLower(c.Owner)
	c.CTag = 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(collectionsBucket)
		if b.Get([]byte(c.Ref().key())) != nil {
			return ErrExists
		}
		if err := stamp(b, &c); err != nil {
			return err
		}
		if err := put(b, []byte(c.Ref().key()), &c); err != nil {
			return err
		}
		return publish(tx, "", c)
	})
	return &c, err
}

func (s *Store) Collection(ref Ref) (*Collection, error) {
	var c *Collection
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		c, err = get[Collection](tx.Bucket(collectionsBucket), []byte(ref.key()))
		return err
	})
	return c, err
}

// Update changes a collection's own properties.
func (s *Store) Update(ref Ref, change func(*Collection)) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(collectionsBucket)
		c, err := get[Collection](b, []byte(ref.key()))
		if err != nil {
			return err
		}
		before := c.Public
		change(c)
		if err := stamp(b, c); err != nil {
			return err
		}
		if err := put(b, []byte(ref.key()), c); err != nil {
			return err
		}
		return publish(tx, before, *c)
	})
}

// Delete removes a collection with its objects and its shares.
func (s *Store) Delete(ref Ref) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		c, err := get[Collection](tx.Bucket(collectionsBucket), []byte(ref.key()))
		if err != nil {
			return err
		}
		before := c.Public
		c.Public = ""
		if err := publish(tx, before, *c); err != nil {
			return err
		}
		for bucket, prefix := range map[*bolt.Bucket][]byte{
			tx.Bucket(objectsBucket): []byte(ref.key() + "/"),
			tx.Bucket(sharesBucket):  []byte(ref.key() + "\x00"),
		} {
			cur := bucket.Cursor()
			for k, _ := cur.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = cur.Next() {
				if err := cur.Delete(); err != nil {
					return err
				}
			}
		}
		return tx.Bucket(collectionsBucket).Delete([]byte(ref.key()))
	})
}

// Visible are the collections of one kind an account sees: its own, and
// those shared with it that it accepted and that have not run out.
// Each comes with what the account may do with it, read in the same
// transaction: a share that runs out while a home is being listed is in
// the listing or out of it, and never an error in the middle.
func (s *Store) Visible(account string, kind Kind, now time.Time) ([]Seen, error) {
	account = strings.ToLower(account)
	var out []Seen
	err := s.db.View(func(tx *bolt.Tx) error {
		collections := tx.Bucket(collectionsBucket)
		err := each(collections, []byte(account+"/"), func(c Collection) error {
			if c.Kind == kind {
				out = append(out, Seen{c, own})
			}
			return nil
		})
		if err != nil {
			return err
		}
		return each(tx.Bucket(sharesBucket), nil, func(sh Share) error {
			if !strings.EqualFold(sh.To, account) || !sh.Live(now) {
				return nil
			}
			c, err := get[Collection](collections, []byte(sh.Ref().key()))
			if err != nil {
				return err
			}
			if c.Kind == kind {
				out = append(out, Seen{*c, accessOf(sh)})
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
// transaction that acts on the answer: a share revoked a moment ago
// gives nothing. A collection the account cannot see is not found,
// which is what it is to that account.
func allowed(tx *bolt.Tx, ref Ref, account string, now time.Time) (*Collection, access, error) {
	c, err := get[Collection](tx.Bucket(collectionsBucket), []byte(ref.key()))
	if err != nil {
		return nil, none, err
	}
	if c.Owner == account {
		return c, own, nil
	}
	sh, err := get[Share](tx.Bucket(sharesBucket), shareKey(ref, account))
	if err != nil {
		return nil, none, err
	}
	if !sh.Live(now) {
		return nil, none, ErrNotFound
	}
	return c, accessOf(*sh), nil
}

// ErrForbidden is a write by an account the collection is shared with
// to read.
var ErrForbidden = errors.New("collections: not yours to change")

// ByPublic finds the collection a public URL's secret names.
func (s *Store) ByPublic(secret string) (*Collection, error) {
	var found *Collection
	err := s.db.View(func(tx *bolt.Tx) (err error) {
		key := tx.Bucket(publicBucket).Get([]byte(secret))
		if secret == "" || key == nil {
			return ErrNotFound
		}
		found, err = get[Collection](tx.Bucket(collectionsBucket), key)
		return err
	})
	return found, err
}

func (s *Store) Objects(ref Ref) ([]Object, error) {
	var out []Object
	err := s.db.View(func(tx *bolt.Tx) error {
		return each(tx.Bucket(objectsBucket), []byte(ref.key()+"/"), func(o Object) error {
			out = append(out, o)
			return nil
		})
	})
	return out, err
}

func (s *Store) Object(ref Ref, name string) (*Object, error) {
	var o *Object
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		o, err = get[Object](tx.Bucket(objectsBucket), objectKey(ref, name))
		return err
	})
	return o, err
}

// ErrPrecondition is a write whose If-Match or If-None-Match did not
// hold: somebody else wrote first.
var ErrPrecondition = errors.New("collections: precondition failed")

// Unless is a write's condition: given the ETag of the object held,
// empty when none is, it says why the write is off, ErrPrecondition when
// somebody else wrote first. Nil is a write that does not care.
type Unless func(held string) error

// PutObject writes an object as the account by.
func (s *Store) PutObject(ref Ref, name string, data []byte, unless Unless, by string) (*Object, error) {
	now := time.Now().UTC()
	o := &Object{Name: name, Data: data, ETag: ETagOf(data), Modified: now}
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := writable(tx, ref, by, now); err != nil {
			return err
		}
		b := tx.Bucket(objectsBucket)
		old, err := get[Object](b, objectKey(ref, name))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		held := ""
		if old != nil {
			held = old.ETag
		}
		if unless != nil {
			if err := unless(held); err != nil {
				return err
			}
		}
		if err := put(b, objectKey(ref, name), o); err != nil {
			return err
		}
		return touch(tx, ref)
	})
	return o, err
}

// writable refuses a write by an account that may not make it.
func writable(tx *bolt.Tx, ref Ref, by string, now time.Time) error {
	_, may, err := allowed(tx, ref, strings.ToLower(by), now)
	if err == nil && may < write {
		err = ErrForbidden
	}
	return err
}

// RemoveObject deletes an object as the account by.
func (s *Store) RemoveObject(ref Ref, name string, by string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := writable(tx, ref, by, time.Now()); err != nil {
			return err
		}
		b := tx.Bucket(objectsBucket)
		if b.Get(objectKey(ref, name)) == nil {
			return ErrNotFound
		}
		if err := b.Delete(objectKey(ref, name)); err != nil {
			return err
		}
		return touch(tx, ref)
	})
}

// PutShare keeps a share, replacing one to the same address.
func (s *Store) PutShare(sh Share) error {
	sh.To = strings.ToLower(sh.To)
	return s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(collectionsBucket).Get([]byte(sh.Ref().key())) == nil {
			return ErrNotFound
		}
		return put(tx.Bucket(sharesBucket), shareKey(sh.Ref(), sh.To), &sh)
	})
}

func (s *Store) Share(ref Ref, to string) (*Share, error) {
	var sh *Share
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		sh, err = get[Share](tx.Bucket(sharesBucket), shareKey(ref, to))
		return err
	})
	return sh, err
}

// RemoveShare ends a share: the owner revoking it, or the invitee
// declining or leaving.
func (s *Store) RemoveShare(ref Ref, to string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(sharesBucket)
		if b.Get(shareKey(ref, to)) == nil {
			return ErrNotFound
		}
		return b.Delete(shareKey(ref, to))
	})
}

// Shares are every share matching keep: a collection's, or an account's.
func (s *Store) Shares(keep func(Share) bool) ([]Share, error) {
	var out []Share
	err := s.db.View(func(tx *bolt.Tx) error {
		return each(tx.Bucket(sharesBucket), nil, func(sh Share) error {
			if keep(sh) {
				out = append(out, sh)
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].To < out[j].To })
	return out, err
}
