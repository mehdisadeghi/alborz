package alborz

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/record"
	"github.com/fernet/fernet-go"
)

// The owners of the records kept here, each the prefix of its keys (ADR 51).
const (
	sessionsOwner = "alborz.sessions"
	readingOwner  = "alborz.reading"
	settingsOwner = "alborz.settings"
	recordsFormat = 1
)

func sessionRecordKey(id string) string { return sessionsOwner + "/" + id }

// accountKey keys an account's record under owner, hashed so the file
// names nobody.
func accountKey(owner, account string) string {
	sum := sha256.Sum256([]byte(account))
	return owner + "/" + hex.EncodeToString(sum[:])
}

// openTimeout bounds the wait for the file's lock.
const openTimeout = 5 * time.Second

// vault reads and writes records sealed with the login key.
type vault struct {
	db  kv.DB
	key *fernet.Key
}

// open is nil for a record sealed with a key that has since rotated,
// which signs everyone out and loses nothing else.
func (v vault) open(sealed []byte) []byte {
	if sealed == nil {
		return nil
	}
	return fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{v.key})
}

func (v vault) put(tx kv.Tx, k string, raw []byte) error {
	sealed, err := fernet.EncryptAndSign(raw, v.key)
	if err != nil {
		return err
	}
	return tx.Put(k, sealed)
}

func (v vault) load(k string, out any) bool {
	var raw []byte
	err := v.db.View(func(tx kv.Tx) error {
		raw = v.open(tx.Get(k))
		return nil
	})
	return err == nil && raw != nil && json.Unmarshal(raw, out) == nil
}

// save writes rec over what is stored under k, keeping what a newer
// version put there (ADR 26).
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

// CheckKey refuses the file at path unless key opens it. A file without the
// check gets one once every value under the sealed prefixes opens.
func CheckKey(db kv.DB, path string, key *fernet.Key, sealed ...string) error {
	keys := []*fernet.Key{key}
	wrong := fmt.Errorf("the login key does not open %s: start alborz with the key that sealed it, "+
		"or move the file aside", path)
	return db.Update(func(tx kv.Tx) error {
		if check := tx.Get(keyCheckKey); check != nil {
			if fernet.VerifyAndDecrypt(check, 0, keys) == nil {
				return wrong
			}
			return nil
		}
		for _, prefix := range sealed {
			err := tx.Scan(prefix, func(_ string, v []byte) error {
				if fernet.VerifyAndDecrypt(v, 0, keys) == nil {
					return wrong
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		check, err := fernet.EncryptAndSign([]byte(keyCheckKey), key)
		if err != nil {
			return err
		}
		return tx.Put(keyCheckKey, check)
	})
}

// records keeps remembered sessions and what each account's reader
// reads by.
type records struct{ vault }

// OpenSessionRecords keeps sessions in the data file.
func OpenSessionRecords(db kv.DB, key *fernet.Key) SessionRecords {
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
			if raw := r.open(sealed); raw != nil && json.Unmarshal(raw, &rec) == nil {
				f(&rec)
			}
			return nil
		})
	})
}

// Sweep drops the sessions nobody has come back to.
func (r *records) Sweep(before time.Time) error {
	return r.db.Update(func(tx kv.Tx) error {
		var stale []string
		err := tx.Scan(sessionsOwner+"/", func(k string, sealed []byte) error {
			var rec SessionRecord
			raw := r.open(sealed)
			if raw == nil || json.Unmarshal(raw, &rec) != nil || rec.Seen.Before(before) {
				stale = append(stale, k)
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, k := range stale {
			if err := tx.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *records) LoadReading(account string) (*Reading, bool) {
	var rd Reading
	if !r.load(accountKey(readingOwner, account), &rd) {
		return nil, false
	}
	return &rd, true
}

func (r *records) SaveReading(account string, rd *Reading) error {
	return r.save(accountKey(readingOwner, account), rd)
}

func (r *records) DeleteReading(account string) error {
	return r.remove(accountKey(readingOwner, account))
}

// settingsRecords keeps the settings of accounts whose server has no
// METADATA, one record per account holding every key.
type settingsRecords struct{ vault }

func openSettingsRecords(db kv.DB, key *fernet.Key) *settingsRecords {
	return &settingsRecords{vault{db: db, key: key}}
}

func (r *settingsRecords) entries(tx kv.Tx, account string) map[string]json.RawMessage {
	entries := map[string]json.RawMessage{}
	if raw := r.open(tx.Get(accountKey(settingsOwner, account))); raw != nil {
		json.Unmarshal(raw, &entries)
	}
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

// Put merges v into the entry as stored at the moment of writing, in
// the one transaction that writes it.
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
