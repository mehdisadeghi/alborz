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
	settingsOwner = "alborz.settings"
	recordsFormat = 1
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
	key *fernet.Key
}

func (v vault) open(sealed []byte) []byte {
	return fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{v.key})
}

func (v vault) put(tx kv.Tx, k string, raw []byte) error {
	sealed, err := fernet.EncryptAndSign(raw, v.key)
	if err != nil {
		return err
	}
	return tx.Put(k, sealed)
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

type records struct{ vault }

// openSessionRecords keeps sessions in the data file.
func openSessionRecords(db kv.DB, key *fernet.Key) sessionRecords {
	return &records{vault{db: db, key: key}}
}

func (r *records) Load(id string) (*sessionRecord, bool) {
	var rec sessionRecord
	if !r.load(sessionRecordKey(id), &rec) {
		return nil, false
	}
	return &rec, true
}

func (r *records) Save(rec *sessionRecord) error { return r.save(sessionRecordKey(rec.ID), rec) }

func (r *records) Delete(id string) error { return r.remove(sessionRecordKey(id)) }

func (r *records) Each(f func(*sessionRecord)) error {
	return r.db.View(func(tx kv.Tx) error {
		return tx.Scan(sessionsOwner+"/", func(_ string, sealed []byte) error {
			var rec sessionRecord
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
			var rec sessionRecord
			return r.read(sealed, &rec) && rec.Seen.Before(before)
		})
	})
}

// settingsRecords keeps settings for servers without METADATA, one record per account.
type settingsRecords struct{ vault }

func openSettingsRecords(db kv.DB, key *fernet.Key) *settingsRecords {
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
