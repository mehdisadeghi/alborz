package alborz

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/record"
	"github.com/fernet/fernet-go"
)

// The owners of the records kept here, each the prefix of its keys, and
// the format they are written in (ADR 26). Every record is sealed with
// the login key, so the file names nobody, the way the DAV cache seals
// its own.
const (
	// sessionsOwner holds one record per session.
	sessionsOwner = "alborz.sessions"
	// readingOwner holds what a person reads by, under the account they
	// anchored it to.
	readingOwner = "alborz.reading"
	// settingsOwner holds the settings of an account whose server has no
	// METADATA.
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

// openTimeout bounds the wait for the file's lock; every network and
// file wait in alborz is bounded and says how long it waited.
const openTimeout = 5 * time.Second

// records keeps sessions across a restart. A reader who asked to be
// remembered is remembered by the server, so the browser carries an id
// and a secret rather than a copy of their passwords.
type records struct {
	db  kv.DB
	key *fernet.Key
}

// OpenSessionRecords keeps sessions in the data file.
func OpenSessionRecords(db kv.DB, key *fernet.Key) SessionRecords {
	return &records{db: db, key: key}
}

// load unseals the record under k into v. A record sealed with a key
// that has since rotated does not read, which signs everyone out and
// loses nothing else.
func (r *records) load(k string, v any) bool {
	var raw []byte
	err := r.db.View(func(tx kv.Tx) error {
		if sealed := tx.Get(k); sealed != nil {
			raw = fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{r.key})
		}
		return nil
	})
	return err == nil && raw != nil && json.Unmarshal(raw, v) == nil
}

// save writes v sealed, over what is stored under k and keeping what a
// newer version put there (ADR 26). A record sealed with a key that has
// since rotated is not read, and is replaced.
func (r *records) save(k string, v any) error {
	return r.db.Update(func(tx kv.Tx) error {
		var old []byte
		if sealed := tx.Get(k); sealed != nil {
			old = fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{r.key})
		}
		raw, err := record.Keep(old, v)
		if err != nil {
			return err
		}
		sealed, err := fernet.EncryptAndSign(raw, r.key)
		if err != nil {
			return err
		}
		return tx.Put(k, sealed)
	})
}

func (r *records) remove(k string) error {
	return r.db.Update(func(tx kv.Tx) error { return tx.Delete(k) })
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
			raw := fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{r.key})
			var rec SessionRecord
			if raw != nil && json.Unmarshal(raw, &rec) == nil {
				f(&rec)
			}
			return nil
		})
	})
}

// Sweep drops the sessions nobody has come back to. A record outlives the
// process, so without this the file only grows.
func (r *records) Sweep(before time.Time) error {
	return r.db.Update(func(tx kv.Tx) error {
		var stale []string
		err := tx.Scan(sessionsOwner+"/", func(k string, sealed []byte) error {
			raw := fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{r.key})
			var rec SessionRecord
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

func (r *records) LoadSettings(account string) (map[string]json.RawMessage, bool) {
	entries := map[string]json.RawMessage{}
	if !r.load(accountKey(settingsOwner, account), &entries) {
		return nil, false
	}
	return entries, true
}

func (r *records) SaveSettings(account string, entries map[string]json.RawMessage) error {
	return r.save(accountKey(settingsOwner, account), entries)
}
