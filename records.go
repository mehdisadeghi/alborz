package alborz

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

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

// accountKey is hashed so the file names nobody.
func accountKey(owner, account string) string {
	sum := sha256.Sum256([]byte(account))
	return owner + "/" + hex.EncodeToString(sum[:])
}

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
	v.db.View(func(tx kv.Tx) error {
		ok = v.read(tx.Get(k), out)
		return nil
	})
	return ok
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
// check gets one once every value under the sealed prefixes opens with its key.
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
		return kv.DeleteUnder(tx, sessionsOwner+"/", func(sealed []byte) bool {
			var rec SessionRecord
			return r.read(sealed, &rec) && rec.Seen.Before(before)
		})
	})
}

// settingsRecords keeps settings for servers without METADATA, one record per account.
type settingsRecords struct{ vault }

func openSettingsRecords(db kv.DB, key *seal.Key) *settingsRecords {
	return &settingsRecords{vault{db: db, key: key}}
}

func (r *settingsRecords) entries(tx kv.Tx, account string) map[string]json.RawMessage {
	entries := map[string]json.RawMessage{}
	r.read(tx.Get(accountKey(settingsOwner, account)), &entries)
	return entries
}

func (r *settingsRecords) Get(account, key string, out any) error {
	var raw json.RawMessage
	err := r.db.View(func(tx kv.Tx) error {
		raw = r.entries(tx, account)[key]
		return nil
	})
	if err != nil {
		return err
	}
	if raw == nil {
		return ErrNoStoreEntry
	}
	return json.Unmarshal(raw, out)
}

// Put merges v into the stored entry in one transaction.
func (r *settingsRecords) Put(account, key string, v any) error {
	return r.db.Update(func(tx kv.Tx) error {
		entries := r.entries(tx, account)
		merged, err := record.Keep(entries[key], v)
		if err != nil {
			return err
		}
		entries[key] = merged
		raw, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		return r.put(tx, accountKey(settingsOwner, account), raw)
	})
}

func (r *settingsRecords) Entries(account string) ([]string, error) {
	var names []string
	err := r.db.View(func(tx kv.Tx) error {
		for name := range r.entries(tx, account) {
			names = append(names, name)
		}
		return nil
	})
	slices.Sort(names)
	return names, err
}

func (r *settingsRecords) Forget(account string) error {
	return r.remove(accountKey(settingsOwner, account))
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
