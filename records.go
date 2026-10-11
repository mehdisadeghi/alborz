package alborz

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"git.mehdix.org/alborz/internal/keyring"
	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/record"
	"git.mehdix.org/alborz/internal/seal"
)

// The owners of the records kept here, each the prefix of its keys (ADR 51).
const (
	sessionsOwner = "alborz.sessions"
	settingsOwner = "alborz.settings"
	recordsFormat = 2
)

func sessionRecordKey(id string) string { return sessionsOwner + "/" + id }

const openTimeout = 5 * time.Second

type vault struct {
	db  kv.DB
	key *seal.Key
}

func (v vault) open(sealed []byte) []byte {
	raw, _ := v.key.Open(sealed)
	return raw
}

func (v vault) put(tx kv.Tx, k string, raw []byte) error {
	return tx.Put(k, v.key.Seal(raw))
}

func (v vault) read(sealed []byte, out any) bool {
	raw := v.open(sealed)
	return raw != nil && json.Unmarshal(raw, out) == nil
}

func (v vault) load(k string, out any) (ok bool) {
	err := v.db.View(func(tx kv.Tx) error {
		ok = v.read(tx.Get(k), out)
		return nil
	})
	return err == nil && ok
}

// save keeps what a newer version put there (ADR 26).
func (v vault) save(k string, rec any) error {
	return v.db.Update(func(tx kv.Tx) error {
		raw, err := record.Keep(v.open(tx.Get(k)), rec)
		if err != nil {
			return err
		}
		return v.put(tx, k, raw)
	})
}

func (v vault) remove(k string) error {
	return v.db.Update(func(tx kv.Tx) error { return tx.Delete(k) })
}

// keyCheckKey holds a value sealed under the login key, so a file opened
// under another key is refused instead of read as empty and overwritten.
const keyCheckKey = "alborz.keycheck"

// CheckKey refuses the file at path unless root sealed it. A file without the
// check gets one once every value under the sealed prefixes opens with its key;
// a value under an account's own key is that account's to open (ADR 72).
func CheckKey(db kv.DB, path string, root seal.Root, sealed map[string]*seal.Key) error {
	refused := func(err error) error {
		if errors.Is(err, seal.ErrFormat) {
			return fmt.Errorf("%s was written by an earlier alborz, in a format this one does not read: "+
				"move the file aside", path)
		}
		return fmt.Errorf("the login key does not open %s: start alborz with the key that sealed it, "+
			"or move the file aside", path)
	}
	key := root.Key(keyCheckKey)
	return db.Update(func(tx kv.Tx) error {
		if check := tx.Get(keyCheckKey); check != nil {
			if _, err := key.Open(check); err != nil {
				return refused(err)
			}
			return nil
		}
		for prefix, k := range sealed {
			err := tx.Scan(prefix, func(_ string, v []byte) error {
				if seal.Format(v) == seal.ByAccount {
					return nil
				}
				_, err := k.Open(v)
				return err
			})
			if err != nil {
				return refused(err)
			}
		}
		return tx.Put(keyCheckKey, key.Seal([]byte(keyCheckKey)))
	})
}

type records struct{ vault }

// openSessionRecords keeps sessions in the data file.
func openSessionRecords(db kv.DB, key *seal.Key) SessionRecords {
	return &records{vault{db: db, key: key}}
}

func (r *records) Load(id string) (*SessionRecord, bool) {
	var rec SessionRecord
	if !r.load(sessionRecordKey(id), &rec) {
		return nil, false
	}
	return &rec, true
}

func (r *records) Save(rec *SessionRecord) error { return r.save(sessionRecordKey(rec.ID), rec) }

func (r *records) Delete(id string) error { return r.remove(sessionRecordKey(id)) }

func (r *records) Each(f func(*SessionRecord)) error {
	return r.db.View(func(tx kv.Tx) error {
		return tx.Scan(sessionsOwner+"/", func(_ string, sealed []byte) error {
			var rec SessionRecord
			if r.read(sealed, &rec) {
				f(&rec)
			}
			return nil
		})
	})
}

// Sweep drops the sessions not seen since before.
func (r *records) Sweep(before time.Time) error {
	return r.db.Update(func(tx kv.Tx) error {
		return kv.DeleteUnder(tx, sessionsOwner+"/", func(_ string, sealed []byte) bool {
			var rec SessionRecord
			return r.read(sealed, &rec) && rec.Seen.Before(before)
		})
	})
}

// settingsRecords keeps settings for servers without METADATA, one record per account, sealed under the
// key the ring gives the account (ADR 72). The account's own key is kept apart, under the instance's key:
// it has to be read before anything under it can be.
type settingsRecords struct {
	db    kv.DB
	keys  *keyring.Ring
	names seal.Names
	apart vault
}

func openSettingsRecords(db kv.DB, key *seal.Key, names seal.Names, keys *keyring.Ring) *settingsRecords {
	return &settingsRecords{db: db, keys: keys, names: names, apart: vault{db: db, key: key}}
}

// A keyed name, so the file names nobody to whoever lacks the login key.
func (r *settingsRecords) key(account string) string {
	return settingsOwner + "/" + r.names.Of(account)
}

func (r *settingsRecords) keyEntryKey(account string) string {
	return settingsOwner + "/key/" + r.names.Of(account)
}

func (r *settingsRecords) entries(tx kv.Tx, account string) (map[string]json.RawMessage, error) {
	entries := map[string]json.RawMessage{}
	sealed := tx.Get(r.key(account))
	if sealed == nil {
		return entries, nil
	}
	raw, err := r.keys.Open(r.keys.Name(account), settingsOwner, sealed)
	if err != nil {
		return nil, err
	}
	return entries, json.Unmarshal(raw, &entries)
}

// Get reads an entry a key at hand does not open as absent.
func (r *settingsRecords) Get(account, key string, out any) error {
	var raw json.RawMessage
	err := r.db.View(func(tx kv.Tx) error {
		entries, err := r.entries(tx, account)
		raw = entries[key]
		return err
	})
	if raw == nil || err != nil {
		return ErrNoStoreEntry
	}
	return json.Unmarshal(raw, out)
}

// Put merges v into the stored entry in one transaction; nothing is written over a record that does not open.
func (r *settingsRecords) Put(account, key string, v any) error {
	return r.db.Update(func(tx kv.Tx) error {
		entries, err := r.entries(tx, account)
		if err != nil {
			return err
		}
		merged, err := record.Keep(entries[key], v)
		if err != nil {
			return err
		}
		entries[key] = merged
		raw, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		k, err := r.keys.For(r.keys.Name(account), settingsOwner)
		if err != nil {
			return err
		}
		return tx.Put(r.key(account), k.Seal(raw))
	})
}

func (r *settingsRecords) Entries(account string) ([]string, error) {
	var names []string
	err := r.db.View(func(tx kv.Tx) error {
		entries, err := r.entries(tx, account)
		for name := range entries {
			names = append(names, name)
		}
		return err
	})
	slices.Sort(names)
	return names, err
}

// Forget keeps the account's own key, which is not a setting but what opens its data.
func (r *settingsRecords) Forget(account string) error {
	return r.apart.remove(r.key(account))
}

// reseal moves the account's record under the key the ring gives it now.
func (r *settingsRecords) reseal(account string) error {
	name := r.keys.Name(account)
	return r.db.Update(func(tx kv.Tx) error {
		sealed := tx.Get(r.key(account))
		k, err := r.keys.For(name, settingsOwner)
		if sealed == nil || err != nil || !r.keys.Stale(name, settingsOwner, sealed) {
			return err
		}
		raw, err := r.keys.Open(name, settingsOwner, sealed)
		if err != nil {
			return err
		}
		return tx.Put(r.key(account), k.Seal(raw))
	})
}

// drop deletes the account's record where it is under an own key that is lost.
func (r *settingsRecords) drop(account string) error {
	return r.db.Update(func(tx kv.Tx) error {
		sealed := tx.Get(r.key(account))
		if sealed == nil {
			return nil
		}
		if _, err := r.keys.Open(r.keys.Name(account), settingsOwner, sealed); !errors.Is(err, keyring.ErrLocked) {
			return nil
		}
		return tx.Delete(r.key(account))
	})
}

// SessionRecord is a session kept across a restart.
type SessionRecord struct {
	ID       string
	Created  time.Time
	Seen     time.Time
	Proof    []byte
	Accounts []RememberedAccount
	Profile  *Profile
	Lock     Lock
	Device   string `json:",omitempty"`
	Address  string `json:",omitempty"`
}

// RememberedAccount is one account of a remembered session.
type RememberedAccount struct {
	Address string
	Sealed  []byte
}

// SessionRecords keeps remembered sessions.
type SessionRecords interface {
	Load(id string) (*SessionRecord, bool)
	Save(rec *SessionRecord) error
	Delete(id string) error
	Sweep(before time.Time) error
	Each(func(*SessionRecord)) error
}
