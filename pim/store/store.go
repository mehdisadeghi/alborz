// Package store keeps alborz's own calendars, task lists and address books; see ADR 24.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz/internal/keyring"
	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/record"
	"git.mehdix.org/alborz/internal/seal"
)

// ErrNotFound is a collection, object or share that is not there.
var ErrNotFound = errors.New("collections: not found")

// Kind is what a collection holds: iCalendar objects or vCards.
type Kind string

const (
	Calendar    Kind = "calendar"
	AddressBook Kind = "addressbook"
)

// Ref names a collection; names are unique per owner, not across owners.
type Ref struct {
	Owner, ID string
}

func (r Ref) key() string { return strings.ToLower(r.Owner) + "/" + r.ID }

// ErrExists is a collection made where one already is.
var ErrExists = errors.New("collections: already exists")

// Collection is one calendar, task list or address book.
type Collection struct {
	ID          string
	Owner       string
	Kind        Kind
	Components  []string `json:",omitempty"`
	Name        string
	Color       string `json:",omitempty"`
	Description string `json:",omitempty"`
	// CTag changes with every write to the collection or its objects.
	CTag uint64
	// Public is the secret in the URL that serves the collection to anyone.
	Public string `json:",omitempty"`
	// Timezone is a calendar's own, as a client set it.
	Timezone []byte `json:",omitempty"`
	// Dead are properties clients keep on a collection, handed back unread.
	Dead []Dead `json:",omitempty"`
	// Feed fills a subscribed calendar; nobody writes its objects.
	Feed *Feed `json:",omitempty"`
}

// Feed is the address a calendar is filled from and what its last fetch said.
type Feed struct {
	URL          string
	ETag         string        `json:",omitempty"`
	LastModified string        `json:",omitempty"`
	Fetched      time.Time     `json:",omitzero"`
	Every        time.Duration `json:",omitempty"`
	// Named is when the calendar's name or colour last changed, so the newer of it and the account's list wins.
	Named time.Time `json:",omitzero"`
}

// Dropped is a fed calendar deleted, kept until the account's list of subscriptions has been told.
type Dropped struct {
	ID, URL string
	At      time.Time
}

// Dead is one such property: its name and the element as it was sent.
type Dead struct {
	Space, Local string
	XML          []byte
}

// Object is one event, task or card as written, and by whom.
type Object struct {
	Name string
	// UID is the object's UID as its parser read it.
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
	// Declined and Left are the invitee's no, before and after accepting.
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

// Ended reports a share past its last day.
func (s Share) Ended() bool {
	return !s.Expires.IsZero() && !time.Now().Before(s.Expires)
}

// Name owns this package's keys in the data file; Format is their format (ADR 26).
const (
	Name   = "alborz.pim"
	Format = 2
)

// keys names the records; every part is a keyed name, so the file names nobody to whoever lacks the login key.
type keys struct{ names seal.Names }

func (k keys) ref(ref Ref) string {
	return k.names.Of(strings.ToLower(ref.Owner)) + "/" + k.names.Of(ref.ID)
}

func (k keys) collectionKey(ref Ref) string { return k.collectionsOf(ref.Owner) + k.names.Of(ref.ID) }

func (k keys) collectionsOf(owner string) string {
	return Name + "/collections/" + k.names.Of(strings.ToLower(owner)) + "/"
}

func (k keys) objectKey(ref Ref, name string) string { return k.objectsOf(ref) + k.names.Of(name) }

func (k keys) objectsOf(ref Ref) string { return Name + "/objects/" + k.ref(ref) + "/" }

func (k keys) shareKey(ref Ref, to string) string {
	return k.sharesOf(ref) + k.names.Of(strings.ToLower(to))
}

func (k keys) sharesOf(ref Ref) string { return Name + "/shares/" + k.ref(ref) + "\x00" }

// Lets a write learn of a UID clash without reading every object.
func (k keys) uidKey(ref Ref, uid string) string { return k.uidsOf(ref) + k.names.Of(uid) }

func (k keys) uidsOf(ref Ref) string { return Name + "/uids/" + k.ref(ref) + "\x00" }

// A wrong guess reads one key.
func (k keys) publicKey(secret string) string { return Name + "/public/" + k.names.Of(secret) }

// The value is the collection's Ref, which the key no longer tells.
func (k keys) receivedKey(to string, ref Ref) string { return k.receivedBy(to) + k.ref(ref) }

func (k keys) receivedBy(to string) string {
	return Name + "/received/" + k.names.Of(strings.ToLower(to)) + "\x00"
}

func (k keys) droppedKey(ref Ref) string { return k.droppedOf(ref.Owner) + k.names.Of(ref.ID) }

func (k keys) droppedOf(owner string) string {
	return Name + "/dropped/" + k.names.Of(strings.ToLower(owner)) + "/"
}

// Counted across collections, so a name made again cannot reuse ctags a client holds.
const ctagKey = Name + "/ctag"

// Store keeps collections in the data file, sealed under a key of the login key's or, while the owner's
// encryption is on, of the collection's own; every method is one transaction.
type Store struct {
	db   sealed
	ring *keyring.Ring
	keys
	watchers []func(accounts []string)
}

func New(db kv.DB, root seal.Root, ring *keyring.Ring) *Store {
	return &Store{db: sealed{db: db, key: root.Key(Name), ring: ring}, ring: ring, keys: keys{root.Names()}}
}

// FeedRef names a subscription's calendar by its address, so two pages making it at once make one; a keyed
// name, as every name in the file is.
func (s *Store) FeedRef(owner, url string) Ref {
	return Ref{Owner: strings.ToLower(owner), ID: "feed-" + s.names.Of(url)}
}

// OnChange has watch told, after each write that makes, renames, deletes or shares a collection, the accounts
// that see it, which a phone's write through the DAV server changes as much as a page's. Set before serving.
func (s *Store) OnChange(watch func(accounts []string)) { s.watchers = append(s.watchers, watch) }

// change runs write as one transaction, then tells the watchers who sees ref before it and after.
func (s *Store) change(ref Ref, write func(kv.Tx) error) error {
	seen := []string{ref.Owner}
	sharees := func(tx kv.Tx) error {
		return each(tx, s.sharesOf(ref), func(sh Share) error {
			seen = append(seen, sh.To)
			return nil
		})
	}
	err := s.db.Update(func(tx kv.Tx) error {
		if err := sharees(tx); err != nil {
			return err
		}
		if err := write(tx); err != nil {
			return err
		}
		return sharees(tx)
	})
	if err != nil {
		return err
	}
	for _, watch := range s.watchers {
		watch(seen)
	}
	return nil
}

func (s *Store) publish(tx kv.Tx, before string, c Collection) error {
	if c.ID != "" {
		if err := keyed(tx).publicWrap(s.ref(c.Ref()), c.Public != ""); err != nil {
			return err
		}
	}
	if before != "" && before != c.Public {
		if err := tx.Delete(s.publicKey(before)); err != nil {
			return err
		}
	}
	if c.Public == "" {
		return nil
	}
	return tx.Put(s.publicKey(c.Public), []byte(s.collectionKey(c.Ref())))
}

// ETagOf names the bytes, so the same object written twice keeps its tag.
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

// Keeps what a newer alborz put there (ADR 26).
func put(tx kv.Tx, key string, v any) error {
	raw, err := record.Keep(tx.Get(key), v)
	if err != nil {
		return err
	}
	return tx.Put(key, raw)
}

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

func (s *Store) touch(tx kv.Tx, ref Ref) error {
	c, err := get[Collection](tx, s.collectionKey(ref))
	if err != nil {
		return err
	}
	if err := stamp(tx, c); err != nil {
		return err
	}
	return put(tx, s.collectionKey(ref), c)
}

// Create keeps a new collection under the name given, or one of its own.
func (s *Store) Create(c Collection) (*Collection, error) {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	c.Owner = strings.ToLower(c.Owner)
	err := s.change(c.Ref(), func(tx kv.Tx) error {
		if tx.Get(s.collectionKey(c.Ref())) != nil {
			return ErrExists
		}
		if owner := s.ring.Name(c.Owner); s.ring.On(owner) {
			if err := keyed(tx).encrypt(s.ref(c.Ref()), []string{owner}); err != nil {
				return err
			}
		}
		if err := stamp(tx, &c); err != nil {
			return err
		}
		if err := put(tx, s.collectionKey(c.Ref()), &c); err != nil {
			return err
		}
		if err := tx.Delete(s.droppedKey(c.Ref())); err != nil {
			return err
		}
		return s.publish(tx, "", c)
	})
	return &c, err
}

func (s *Store) Collection(ref Ref) (*Collection, error) {
	var c *Collection
	err := s.db.View(func(tx kv.Tx) (err error) {
		c, err = get[Collection](tx, s.collectionKey(ref))
		return err
	})
	return c, err
}

// Update changes a collection's own properties; a fed calendar's new name or colour is dated unless change dates it.
func (s *Store) Update(ref Ref, change func(*Collection)) error {
	return s.change(ref, func(tx kv.Tx) error {
		c, err := get[Collection](tx, s.collectionKey(ref))
		if err != nil {
			return err
		}
		before, was := c.Public, *c
		var named time.Time
		if c.Feed != nil {
			named = c.Feed.Named
		}
		change(c)
		if c.Feed != nil && (c.Name != was.Name || c.Color != was.Color) && c.Feed.Named.Equal(named) {
			c.Feed.Named = time.Now().UTC()
		}
		if err := stamp(tx, c); err != nil {
			return err
		}
		if err := put(tx, s.collectionKey(ref), c); err != nil {
			return err
		}
		return s.publish(tx, before, *c)
	})
}

// Delete removes a collection with its objects and its shares; a fed calendar leaves a Dropped behind.
func (s *Store) Delete(ref Ref) error {
	return s.change(ref, func(tx kv.Tx) error {
		c, err := get[Collection](tx, s.collectionKey(ref))
		if err != nil {
			return err
		}
		if err := s.publish(tx, c.Public, Collection{}); err != nil {
			return err
		}
		if c.Feed != nil {
			if err := put(tx, s.droppedKey(ref), Dropped{ID: ref.ID, URL: c.Feed.URL, At: time.Now().UTC()}); err != nil {
				return err
			}
		}
		var gone []string
		collect := func(k string, _ []byte) error {
			gone = append(gone, k)
			return nil
		}
		for _, prefix := range []string{s.objectsOf(ref), s.uidsOf(ref)} {
			if err := tx.Scan(prefix, collect); err != nil {
				return err
			}
		}
		err = each(tx, s.sharesOf(ref), func(sh Share) error {
			gone = append(gone, s.shareKey(ref, sh.To), s.receivedKey(sh.To, ref))
			return nil
		})
		if err != nil {
			return err
		}
		for _, k := range append(gone, s.collectionKey(ref)) {
			if err := tx.Delete(k); err != nil {
				return err
			}
		}
		return keyed(tx).dropKey(s.ref(ref))
	})
}

// Visible are the collections of one kind an account sees, with what it may do.
func (s *Store) Visible(account string, kind Kind, now time.Time) ([]Seen, error) {
	account = strings.ToLower(account)
	var out []Seen
	err := s.db.View(func(tx kv.Tx) error {
		err := each(tx, s.collectionsOf(account), func(c Collection) error {
			if c.Kind == kind {
				out = append(out, Seen{c, own})
			}
			return nil
		})
		if err != nil {
			return err
		}
		// A share whose collection's key this account cannot open yet is passed over (ADR 72).
		return each(tx, s.receivedBy(account), func(ref Ref) error {
			sh, err := get[Share](tx, s.shareKey(ref, account))
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			if err != nil || !sh.Live(now) {
				return err
			}
			c, err := get[Collection](tx, s.collectionKey(ref))
			if errors.Is(err, ErrNotFound) {
				return nil
			}
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

func (s *Store) allowed(tx kv.Tx, ref Ref, account string, now time.Time) (*Collection, access, error) {
	c, err := get[Collection](tx, s.collectionKey(ref))
	if err != nil {
		return nil, none, err
	}
	if c.Owner == account {
		return c, own, nil
	}
	sh, err := get[Share](tx, s.shareKey(ref, account))
	if err != nil {
		return nil, none, err
	}
	if !sh.Live(now) {
		return nil, none, ErrNotFound
	}
	return c, accessOf(*sh), nil
}

func (s *Store) may(ref Ref, account string, now time.Time) (c *Collection, may access, err error) {
	err = s.db.View(func(tx kv.Tx) error {
		c, may, err = s.allowed(tx, ref, account, now)
		return err
	})
	return c, may, err
}

// ErrForbidden is a write by an account with read-only access.
var ErrForbidden = errors.New("collections: not yours to change")

// ByPublic finds the collection a public URL's secret names.
func (s *Store) ByPublic(secret string) (*Collection, error) {
	var found *Collection
	err := s.db.View(func(tx kv.Tx) (err error) {
		key := tx.Get(s.publicKey(secret))
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
		return each(tx, s.objectsOf(ref), func(o Object) error {
			out = append(out, o)
			return nil
		})
	})
	return out, err
}

func (s *Store) Object(ref Ref, name string) (*Object, error) {
	var o *Object
	err := s.db.View(func(tx kv.Tx) (err error) {
		o, err = get[Object](tx, s.objectKey(ref, name))
		return err
	})
	return o, err
}

// ErrPrecondition is a write whose If-Match or If-None-Match did not hold.
var ErrPrecondition = errors.New("collections: precondition failed")

// ErrUIDConflict is a second object with a UID the collection already holds.
var ErrUIDConflict = errors.New("collections: UID already in the collection")

// Unless says why a write is off, given the held ETag or empty; nil does not care.
type Unless func(held string) error

// PutObject writes an object as the account by.
func (s *Store) PutObject(ref Ref, name, uid string, data []byte, unless Unless, by string) (*Object, error) {
	now := time.Now().UTC()
	o := &Object{Name: name, UID: uid, Data: data, ETag: ETagOf(data), Modified: now, Creator: by, Created: now, Editor: by}
	err := s.db.Update(func(tx kv.Tx) error {
		if err := s.writable(tx, ref, by, now); err != nil {
			return err
		}
		old, err := get[Object](tx, s.objectKey(ref, name))
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
			if holder := tx.Get(s.uidKey(ref, uid)); holder != nil && string(holder) != name {
				// An index entry is believed only where its object agrees.
				other, err := get[Object](tx, s.objectKey(ref, string(holder)))
				if err != nil && !errors.Is(err, ErrNotFound) {
					return err
				}
				if other != nil && other.UID == uid {
					return ErrUIDConflict
				}
			}
			if err := tx.Put(s.uidKey(ref, uid), []byte(name)); err != nil {
				return err
			}
		}
		if old != nil && old.UID != "" && old.UID != uid {
			if err := tx.Delete(s.uidKey(ref, old.UID)); err != nil {
				return err
			}
		}
		if err := put(tx, s.objectKey(ref, name), o); err != nil {
			return err
		}
		return s.touch(tx, ref)
	})
	return o, err
}

func (s *Store) writable(tx kv.Tx, ref Ref, by string, now time.Time) error {
	c, may, err := s.allowed(tx, ref, strings.ToLower(by), now)
	if err == nil && (may < write || c.Feed != nil) {
		err = ErrForbidden
	}
	return err
}

// Dropped are the owner's fed calendars deleted since the account's list was last told.
func (s *Store) Dropped(owner string) ([]Dropped, error) {
	var out []Dropped
	err := s.db.View(func(tx kv.Tx) error {
		return each(tx, s.droppedOf(owner), func(d Dropped) error {
			out = append(out, d)
			return nil
		})
	})
	return out, err
}

// Told forgets a Dropped once the account's list says so.
func (s *Store) Told(ref Ref) error {
	return s.db.Update(func(tx kv.Tx) error { return tx.Delete(s.droppedKey(ref)) })
}

// Feeds are every calendar filled from a feed, of every account.
func (s *Store) Feeds() ([]Collection, error) {
	var out []Collection
	err := s.db.View(func(tx kv.Tx) error {
		return each(tx, Name+"/collections/", func(c Collection) error {
			if c.Feed != nil {
				out = append(out, c)
			}
			return nil
		})
	})
	return out, err
}

// Fill keeps what a fetch of a fed calendar said, and its objects when the feed sent them: all replaced in one
// write, the ctag moved only when one changed. Nil objects keep those held.
func (s *Store) Fill(ref Ref, feed Feed, objects []Object) error {
	return s.db.Update(func(tx kv.Tx) error {
		c, err := get[Collection](tx, s.collectionKey(ref))
		if err != nil {
			return err
		}
		if c.Feed == nil || c.Feed.URL != feed.URL {
			return fmt.Errorf("collections: %s is not filled from %s", ref.key(), feed.URL)
		}
		feed.Named = c.Feed.Named
		c.Feed = &feed
		if objects != nil {
			if err := s.replace(tx, c, objects); err != nil {
				return err
			}
		}
		return put(tx, s.collectionKey(ref), c)
	})
}

func (s *Store) replace(tx kv.Tx, c *Collection, objects []Object) error {
	ref := c.Ref()
	held := map[string]Object{}
	err := each(tx, s.objectsOf(ref), func(o Object) error {
		held[o.Name] = o
		return nil
	})
	if err != nil {
		return err
	}
	changed := len(held) != len(objects)
	for _, o := range objects {
		changed = changed || held[o.Name].ETag != ETagOf(o.Data)
	}
	if !changed {
		return nil
	}
	for _, o := range held {
		if err := tx.Delete(s.objectKey(ref, o.Name)); err != nil {
			return err
		}
		if err := tx.Delete(s.uidKey(ref, o.UID)); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	for _, o := range objects {
		o.ETag, o.Modified, o.Created = ETagOf(o.Data), now, now
		if was, ok := held[o.Name]; ok {
			o.Created = was.Created
		}
		if err := put(tx, s.objectKey(ref, o.Name), &o); err != nil {
			return err
		}
		if err := tx.Put(s.uidKey(ref, o.UID), []byte(o.Name)); err != nil {
			return err
		}
	}
	return stamp(tx, c)
}

// RemoveObject deletes an object as the account by, unless its condition says not to.
func (s *Store) RemoveObject(ref Ref, name string, unless Unless, by string) error {
	return s.db.Update(func(tx kv.Tx) error {
		if err := s.writable(tx, ref, by, time.Now()); err != nil {
			return err
		}
		old, err := get[Object](tx, s.objectKey(ref, name))
		if err != nil {
			return err
		}
		if unless != nil {
			if err := unless(old.ETag); err != nil {
				return err
			}
		}
		if old.UID != "" {
			if err := tx.Delete(s.uidKey(ref, old.UID)); err != nil {
				return err
			}
		}
		if err := tx.Delete(s.objectKey(ref, name)); err != nil {
			return err
		}
		return s.touch(tx, ref)
	})
}

// PutShare keeps a share, replacing one to the same address.
func (s *Store) PutShare(sh Share) error {
	sh.To = strings.ToLower(sh.To)
	return s.change(sh.Ref(), func(tx kv.Tx) error {
		if tx.Get(s.collectionKey(sh.Ref())) == nil {
			return ErrNotFound
		}
		if err := put(tx, s.shareKey(sh.Ref(), sh.To), &sh); err != nil {
			return err
		}
		if err := keyed(tx).wrapTo(s.ref(sh.Ref()), s.ring.Name(sh.To)); err != nil {
			return err
		}
		return put(tx, s.receivedKey(sh.To, sh.Ref()), sh.Ref())
	})
}

func (s *Store) Share(ref Ref, to string) (*Share, error) {
	var sh *Share
	err := s.db.View(func(tx kv.Tx) (err error) {
		sh, err = get[Share](tx, s.shareKey(ref, to))
		return err
	})
	return sh, err
}

// Answer is the invitee's yes or no: yes makes it live, no is Declined before and Left after.
func (s *Store) Answer(ref Ref, to string, yes bool) error {
	return s.change(ref, func(tx kv.Tx) error {
		sh, err := get[Share](tx, s.shareKey(ref, to))
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
		return put(tx, s.shareKey(ref, to), sh)
	})
}

// RemoveShare ends a share: the owner revoking it.
func (s *Store) RemoveShare(ref Ref, to string) error {
	return s.change(ref, func(tx kv.Tx) error {
		if tx.Get(s.shareKey(ref, to)) == nil {
			return ErrNotFound
		}
		if err := tx.Delete(s.receivedKey(to, ref)); err != nil {
			return err
		}
		if err := keyed(tx).unwrapFrom(s.ref(ref), s.ring.Name(to)); err != nil {
			return err
		}
		return tx.Delete(s.shareKey(ref, to))
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
