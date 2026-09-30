package store

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"git.mehdix.org/alborz/internal/keyring"
	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/seal"
)

// sealed is db with every value sealed: under the instance's key, or a collection's own while its owner's
// encryption is on (ADR 71). A value that does not open fails a write's transaction, so nothing is written
// over what could not be read; a read passes over one whose key is not at hand, as if it were not there.
type sealed struct {
	db   kv.DB
	key  *seal.Key
	ring *keyring.Ring
}

func (s sealed) View(fn func(kv.Tx) error) error {
	return s.db.View(func(tx kv.Tx) error { return s.run(tx, false, fn) })
}

func (s sealed) Update(fn func(kv.Tx) error) error {
	return s.db.Update(func(tx kv.Tx) error { return s.run(tx, true, fn) })
}

func (s sealed) Close() error { return s.db.Close() }

func (s sealed) run(tx kv.Tx, write bool, fn func(kv.Tx) error) error {
	t := &sealedTx{tx: tx, key: s.key, ring: s.ring, write: write, held: map[string]*collectionKey{}}
	err := fn(t)
	if t.err != nil {
		return t.err
	}
	return err
}

// keyRecord is a collection's own key: wrapped to each account that may read it, and under the instance's
// key while the collection is published, since anyone may then read it.
type keyRecord struct {
	Wraps  map[string]keyring.Wrap
	Public []byte `json:",omitempty"`
}

// collectionKey is what a transaction knows of a collection's key: none, one it opened, or one it cannot.
type collectionKey struct {
	rec    *keyRecord
	secret []byte
	key    *seal.Key
}

func (c *collectionKey) locked() bool { return c.rec != nil && c.key == nil }

// The records a collection's key seals begin with one of these and its scope, the owner's and the
// collection's keyed names.
var scoped = []string{Name + "/collections/", Name + "/objects/", Name + "/shares/", Name + "/uids/"}

const scopeLen = 2*32 + 1

func scopeOf(k string) string {
	for _, prefix := range scoped {
		if rest, ok := strings.CutPrefix(k, prefix); ok && len(rest) >= scopeLen {
			return rest[:scopeLen]
		}
	}
	return ""
}

func keyKey(scope string) string { return Name + "/keys/" + scope }

// info binds a wrap to its collection, so it cannot be moved to another.
func info(scope string) string { return Name + " collection key " + scope }

type sealedTx struct {
	tx    kv.Tx
	key   *seal.Key
	ring  *keyring.Ring
	write bool
	err   error
	held  map[string]*collectionKey
}

func keyed(tx kv.Tx) *sealedTx { return tx.(*sealedTx) }

func (t *sealedTx) collection(scope string) (*collectionKey, error) {
	if c, ok := t.held[scope]; ok {
		return c, nil
	}
	c := &collectionKey{}
	if raw := t.tx.Get(keyKey(scope)); raw != nil {
		plain, err := t.key.Open(raw)
		if err != nil {
			return nil, fmt.Errorf("collections: the key of %s: %w", scope, err)
		}
		c.rec = &keyRecord{}
		if err := json.Unmarshal(plain, c.rec); err != nil {
			return nil, err
		}
		var ok bool
		if c.rec.Public != nil {
			c.secret, err = t.key.Open(c.rec.Public)
			ok = err == nil
		} else {
			c.secret, ok = t.ring.Unwrap(c.rec.Wraps, info(scope))
		}
		if ok && len(c.secret) == seal.KeySize {
			c.key = seal.NewAccountKey([seal.KeySize]byte(c.secret))
		}
	}
	t.held[scope] = c
	return c, nil
}

func (t *sealedTx) open(k string, v []byte) ([]byte, error) {
	var plain []byte
	var err error
	switch {
	case seal.Format(v) != seal.ByAccount:
		plain, err = t.key.Open(v)
	default:
		c, cerr := t.collection(scopeOf(k))
		switch {
		case cerr != nil:
			err = cerr
		case c.key != nil:
			plain, err = c.key.Open(v)
		case c.locked():
			err = keyring.ErrLocked
		default:
			err = seal.ErrOpen
		}
	}
	if err != nil {
		return nil, fmt.Errorf("collections: %s: %w", k, err)
	}
	return plain, nil
}

// passed is a value a read goes on without.
func (t *sealedTx) passed(err error) bool { return !t.write && errors.Is(err, keyring.ErrLocked) }

func (t *sealedTx) Get(k string) []byte {
	v := t.tx.Get(k)
	if v == nil {
		return nil
	}
	plain, err := t.open(k, v)
	if err != nil && !t.passed(err) && t.err == nil {
		t.err = err
	}
	return plain
}

// sealer is the key a value at k goes under.
func (t *sealedTx) sealer(k string) (*seal.Key, error) {
	scope := scopeOf(k)
	if scope == "" {
		return t.key, nil
	}
	c, err := t.collection(scope)
	switch {
	case err != nil:
		return nil, err
	case c.locked():
		return nil, keyring.ErrLocked
	case c.key != nil:
		return c.key, nil
	}
	return t.key, nil
}

func (t *sealedTx) Put(k string, v []byte) error {
	key, err := t.sealer(k)
	if err != nil {
		return err
	}
	return t.tx.Put(k, key.Seal(v))
}

func (t *sealedTx) Delete(k string) error { return t.tx.Delete(k) }

func (t *sealedTx) Scan(prefix string, fn func(k string, v []byte) error) error {
	return t.tx.Scan(prefix, func(k string, v []byte) error {
		plain, err := t.open(k, v)
		if t.passed(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return fn(k, plain)
	})
}

func (t *sealedTx) saveKey(scope string, c *collectionKey) error {
	raw, err := json.Marshal(c.rec)
	if err != nil {
		return err
	}
	return t.tx.Put(keyKey(scope), t.key.Seal(raw))
}

// wrapTo gives the account a wrap of the collection's key, unless it holds a current one, the collection
// has no key, or the account has no pair yet: then its share waits for the next try.
func (t *sealedTx) wrapTo(scope, name string) error {
	c, err := t.collection(scope)
	if err != nil || c.key == nil || c.rec.Wraps[name].To == t.ring.ID(name) && c.rec.Wraps[name].To != "" {
		return err
	}
	w, ok, err := t.ring.Wrap(name, c.secret, info(scope))
	if err != nil || !ok {
		return err
	}
	c.rec.Wraps[name] = w
	return t.saveKey(scope, c)
}

func (t *sealedTx) unwrapFrom(scope, name string) error {
	c, err := t.collection(scope)
	if err != nil || c.rec == nil {
		return err
	}
	delete(c.rec.Wraps, name)
	return t.saveKey(scope, c)
}

// publicWrap keeps the key under the instance's while the collection is published.
func (t *sealedTx) publicWrap(scope string, published bool) error {
	c, err := t.collection(scope)
	switch {
	case err != nil || c.rec == nil || (c.rec.Public != nil) == published:
		return err
	case c.locked():
		return keyring.ErrLocked
	case published:
		c.rec.Public = t.key.Seal(c.secret)
	default:
		c.rec.Public = nil
	}
	return t.saveKey(scope, c)
}

func (t *sealedTx) dropKey(scope string) error {
	delete(t.held, scope)
	return t.tx.Delete(keyKey(scope))
}

func prefixesOf(scope string) []string {
	return []string{Name + "/collections/" + scope, Name + "/objects/" + scope + "/", Name + "/shares/" + scope + "\x00",
		Name + "/uids/" + scope + "\x00"}
}

// values are the collection's records, opened.
func (t *sealedTx) values(scope string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, prefix := range prefixesOf(scope) {
		err := t.tx.Scan(prefix, func(k string, v []byte) error {
			plain, err := t.open(k, v)
			out[k] = plain
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// encrypt gives the collection a key of its own, wrapped to holders, and seals every record under it.
func (t *sealedTx) encrypt(scope string, holders []string) error {
	c, err := t.collection(scope)
	if err != nil || c.rec != nil {
		return err
	}
	return t.rekey(scope, holders)
}

// rekey gives the collection a new key, wrapped to holders, and seals every record under it again, so a
// key pair the owner replaced opens nothing written after.
func (t *sealedTx) rekey(scope string, holders []string) error {
	c, err := t.collection(scope)
	if err != nil {
		return err
	}
	if c.locked() {
		return keyring.ErrLocked
	}
	values, err := t.values(scope)
	if err != nil {
		return err
	}
	published := c.rec != nil && c.rec.Public != nil
	c.secret = make([]byte, seal.KeySize)
	rand.Read(c.secret)
	c.key, c.rec = seal.NewAccountKey([seal.KeySize]byte(c.secret)), &keyRecord{Wraps: map[string]keyring.Wrap{}}
	if published {
		c.rec.Public = t.key.Seal(c.secret)
	}
	for _, name := range holders {
		w, ok, err := t.ring.Wrap(name, c.secret, info(scope))
		if err != nil {
			return err
		}
		if ok {
			c.rec.Wraps[name] = w
		}
	}
	if err := t.saveKey(scope, c); err != nil {
		return err
	}
	for k, v := range values {
		if err := t.tx.Put(k, c.key.Seal(v)); err != nil {
			return err
		}
	}
	return nil
}

// decrypt seals the collection's records under the instance's key again and drops its own.
func (t *sealedTx) decrypt(scope string) error {
	c, err := t.collection(scope)
	if err != nil || c.rec == nil {
		return err
	}
	if c.locked() {
		return keyring.ErrLocked
	}
	values, err := t.values(scope)
	if err != nil {
		return err
	}
	for k, v := range values {
		if err := t.tx.Put(k, t.key.Seal(v)); err != nil {
			return err
		}
	}
	return t.dropKey(scope)
}

// scopes are the scopes named by the keys under prefix, read without opening a value.
func (s *Store) scopes(prefix string) ([]string, error) {
	var out []string
	err := s.db.db.View(func(tx kv.Tx) error {
		return tx.Scan(prefix, func(k string, _ []byte) error {
			out = append(out, strings.TrimPrefix(k, prefix))
			return nil
		})
	})
	return out, err
}

// Reseal brings the account's collections under the key its encryption asks for, each in a transaction of
// its own, so a move cut short goes on at the next sign-in. It gives every account that may read one of
// them, or one shared with it, a wrap of the key where it has none: a share waiting on either side opens.
func (s *Store) Reseal(account string) error {
	owner := strings.ToLower(account)
	name := s.ring.Name(owner)
	own, err := s.scopes(Name + "/collections/" + name + "/")
	if err != nil {
		return err
	}
	received, err := s.scopes(s.receivedBy(owner))
	if err != nil {
		return err
	}
	var errs []error
	for _, coll := range own {
		scope := name + "/" + coll
		errs = append(errs, s.db.Update(func(tx kv.Tx) error {
			t := keyed(tx)
			if !s.ring.On(name) {
				return t.decrypt(scope)
			}
			holders := []string{name}
			err := t.Scan(Name+"/shares/"+scope+"\x00", func(_ string, raw []byte) error {
				var sh Share
				err := json.Unmarshal(raw, &sh)
				holders = append(holders, s.ring.Name(sh.To))
				return err
			})
			if err != nil {
				return err
			}
			c, err := t.collection(scope)
			switch {
			case err != nil:
				return err
			case c.key != nil && c.rec.Wraps[name].To != s.ring.ID(name):
				err = t.rekey(scope, holders)
			default:
				err = t.encrypt(scope, holders)
			}
			if err != nil {
				return err
			}
			for _, holder := range holders[1:] {
				if err := t.wrapTo(scope, holder); err != nil {
					return err
				}
			}
			return nil
		}))
	}
	for _, scope := range received {
		errs = append(errs, s.db.Update(func(tx kv.Tx) error { return keyed(tx).wrapTo(scope, name) }))
	}
	// What the account sees may have changed with the keys at hand.
	s.told(owner)
	return errors.Join(errs...)
}

// Drop deletes the account's collections whose key nothing at hand opens, with their shares and
// publications: the reader starting over after the key was lost.
func (s *Store) Drop(account string) error {
	name := s.ring.Name(account)
	own, err := s.scopes(Name + "/collections/" + name + "/")
	if err != nil {
		return err
	}
	for _, coll := range own {
		scope := name + "/" + coll
		err := s.db.Update(func(tx kv.Tx) error {
			t := keyed(tx)
			c, err := t.collection(scope)
			if err != nil || !c.locked() {
				return err
			}
			for _, prefix := range prefixesOf(scope) {
				if err := kv.DeleteUnder(t.tx, prefix, nil); err != nil {
					return err
				}
			}
			var gone []string
			err = t.tx.Scan(Name+"/received/", func(k string, _ []byte) error {
				if strings.HasSuffix(k, "\x00"+scope) {
					gone = append(gone, k)
				}
				return nil
			})
			if err != nil {
				return err
			}
			err = t.tx.Scan(Name+"/public/", func(k string, v []byte) error {
				if at, err := t.key.Open(v); err == nil && string(at) == Name+"/collections/"+scope {
					gone = append(gone, k)
				}
				return nil
			})
			if err != nil {
				return err
			}
			for _, k := range gone {
				if err := t.tx.Delete(k); err != nil {
					return err
				}
			}
			return t.dropKey(scope)
		})
		if err != nil {
			return err
		}
	}
	s.told(strings.ToLower(account))
	return nil
}

func (s *Store) told(account string) {
	for _, watch := range s.watchers {
		watch([]string{account})
	}
}
