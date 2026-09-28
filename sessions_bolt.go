package alborz

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"git.mehdix.org/alborz/record"
	"github.com/fernet/fernet-go"
	bolt "go.etcd.io/bbolt"
)

// sessionBucket holds one record per session, sealed with the login key so
// the file names nobody, the way the DAV cache seals its own.
var sessionBucket = []byte("sessions")

// readingBucket holds what a person reads by, under the account they
// anchored it to, hashed so the file names nobody.
var readingBucket = []byte("reading")

// settingsBucket holds the settings of an account whose server has no
// METADATA: one sealed record per account, keyed the same hashed way.
var settingsBucket = []byte("settings")

func readingKey(account string) []byte {
	sum := sha256.Sum256([]byte(account))
	return sum[:]
}

// openTimeout bounds the wait for the file's lock; every network and
// file wait in alborz is bounded and says how long it waited.
const openTimeout = 5 * time.Second

// boltSessions keeps sessions across a restart. A reader who asked to be
// remembered is remembered by the server, so the browser carries an id
// and a secret rather than a copy of their passwords.
type boltSessions struct {
	db  *bolt.DB
	key *fernet.Key
}

// OpenSessionRecords keeps sessions in the data file, making their buckets
// if they are not there.
func OpenSessionRecords(db *bolt.DB, key *fernet.Key) (SessionRecords, error) {
	err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{sessionBucket, readingBucket, settingsBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &boltSessions{db: db, key: key}, nil
}

func (b *boltSessions) Load(id string) (*SessionRecord, bool) {
	var sealed []byte
	err := b.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(sessionBucket).Get([]byte(id)); v != nil {
			sealed = append([]byte(nil), v...)
		}
		return nil
	})
	if err != nil || sealed == nil {
		return nil, false
	}
	raw := fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{b.key})
	if raw == nil {
		// A rotated login key makes every record unreadable, which
		// signs everyone out and loses nothing else.
		return nil, false
	}
	var rec SessionRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, false
	}
	return &rec, true
}

func (b *boltSessions) Save(rec *SessionRecord) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return b.putSealed(tx.Bucket(sessionBucket), []byte(rec.ID), rec)
	})
}

// putSealed writes v sealed, over what is stored under key and keeping
// what a newer version put there (ADR 26). A record sealed with a key
// that has since rotated is not read, and is replaced.
func (b *boltSessions) putSealed(bucket *bolt.Bucket, key []byte, v any) error {
	var old []byte
	if sealed := bucket.Get(key); sealed != nil {
		old = fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{b.key})
	}
	raw, err := record.Keep(old, v)
	if err != nil {
		return err
	}
	sealed, err := fernet.EncryptAndSign(raw, b.key)
	if err != nil {
		return err
	}
	return bucket.Put(key, sealed)
}

func (b *boltSessions) Delete(id string) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(sessionBucket).Delete([]byte(id))
	})
}

func (b *boltSessions) Each(f func(*SessionRecord)) error {
	return b.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(sessionBucket).ForEach(func(_, sealed []byte) error {
			raw := fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{b.key})
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
func (b *boltSessions) Sweep(before time.Time) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(sessionBucket)
		var stale [][]byte
		err := bucket.ForEach(func(k, v []byte) error {
			raw := fernet.VerifyAndDecrypt(v, 0, []*fernet.Key{b.key})
			if raw == nil {
				stale = append(stale, append([]byte(nil), k...))
				return nil
			}
			var rec SessionRecord
			if err := json.Unmarshal(raw, &rec); err != nil || rec.Seen.Before(before) {
				stale = append(stale, append([]byte(nil), k...))
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, k := range stale {
			if err := bucket.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func (b *boltSessions) LoadReading(account string) (*Reading, bool) {
	var sealed []byte
	err := b.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(readingBucket).Get(readingKey(account)); v != nil {
			sealed = append([]byte(nil), v...)
		}
		return nil
	})
	if err != nil || sealed == nil {
		return nil, false
	}
	raw := fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{b.key})
	if raw == nil {
		return nil, false
	}
	var r Reading
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, false
	}
	return &r, true
}

func (b *boltSessions) SaveReading(account string, r *Reading) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return b.putSealed(tx.Bucket(readingBucket), readingKey(account), r)
	})
}

func (b *boltSessions) LoadSettings(account string) (map[string]json.RawMessage, bool) {
	var sealed []byte
	err := b.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(settingsBucket).Get(readingKey(account)); v != nil {
			sealed = append([]byte(nil), v...)
		}
		return nil
	})
	if err != nil || sealed == nil {
		return nil, false
	}
	raw := fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{b.key})
	if raw == nil {
		return nil, false
	}
	entries := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, false
	}
	return entries, true
}

func (b *boltSessions) SaveSettings(account string, entries map[string]json.RawMessage) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return b.putSealed(tx.Bucket(settingsBucket), readingKey(account), entries)
	})
}

func (b *boltSessions) DeleteReading(account string) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(readingBucket).Delete(readingKey(account))
	})
}
