package alborz

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"git.mehdix.org/alborz/record"
	"github.com/fernet/fernet-go"
	bolt "go.etcd.io/bbolt"
)

// visitBucket holds one record per visit, sealed with the login key so
// the file names nobody, the way the DAV cache seals its own.
var visitBucket = []byte("visits")

// readingBucket holds what a person reads by, under the account they
// anchored it to, hashed so the file names nobody.
var readingBucket = []byte("reading")

// keptBucket holds what alborz keeps for an account whose server has no
// METADATA: one sealed record per account, keyed the same hashed way.
var keptBucket = []byte("kept")

func readingKey(account string) []byte {
	sum := sha256.Sum256([]byte(account))
	return sum[:]
}

// openTimeout bounds the wait for the file's lock; every network and
// file wait in alborz is bounded and says how long it waited.
const openTimeout = 5 * time.Second

// boltVisits keeps visits across a restart. A reader who asked to be
// remembered is remembered by the server, so the browser carries an id
// and a secret rather than a copy of their passwords.
type boltVisits struct {
	db  *bolt.DB
	key *fernet.Key
}

// OpenVisitRecords keeps visits in the data file, making their buckets
// if they are not there.
func OpenVisitRecords(db *bolt.DB, key *fernet.Key) (VisitRecords, error) {
	err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{visitBucket, readingBucket, keptBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &boltVisits{db: db, key: key}, nil
}

func (b *boltVisits) Load(id string) (*VisitRecord, bool) {
	var sealed []byte
	err := b.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(visitBucket).Get([]byte(id)); v != nil {
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
	var rec VisitRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, false
	}
	return &rec, true
}

func (b *boltVisits) Save(rec *VisitRecord) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return b.putSealed(tx.Bucket(visitBucket), []byte(rec.ID), rec)
	})
}

// putSealed writes v sealed, over what is stored under key and keeping
// what a newer version put there (ADR 26). A record sealed with a key
// that has since rotated is not read, and is replaced.
func (b *boltVisits) putSealed(bucket *bolt.Bucket, key []byte, v any) error {
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

func (b *boltVisits) Delete(id string) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(visitBucket).Delete([]byte(id))
	})
}

// Sweep drops the visits nobody has come back to. A record outlives the
// process, so without this the file only grows.
func (b *boltVisits) Sweep(before time.Time) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(visitBucket)
		var stale [][]byte
		err := bucket.ForEach(func(k, v []byte) error {
			raw := fernet.VerifyAndDecrypt(v, 0, []*fernet.Key{b.key})
			if raw == nil {
				stale = append(stale, append([]byte(nil), k...))
				return nil
			}
			var rec VisitRecord
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

func (b *boltVisits) LoadReading(account string) (*Reading, bool) {
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

func (b *boltVisits) SaveReading(account string, r *Reading) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return b.putSealed(tx.Bucket(readingBucket), readingKey(account), r)
	})
}

func (b *boltVisits) LoadKept(account string) (map[string]json.RawMessage, bool) {
	var sealed []byte
	err := b.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(keptBucket).Get(readingKey(account)); v != nil {
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

func (b *boltVisits) SaveKept(account string, entries map[string]json.RawMessage) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return b.putSealed(tx.Bucket(keptBucket), readingKey(account), entries)
	})
}

func (b *boltVisits) DeleteReading(account string) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(readingBucket).Delete(readingKey(account))
	})
}
