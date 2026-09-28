// Package bolt is kv over a bbolt file, every key in one bucket.
package bolt

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"git.mehdix.org/alborz/internal/kv"
	"go.etcd.io/bbolt"
	bbolterrors "go.etcd.io/bbolt/errors"
)

// bucket holds every key.
var bucket = []byte("kv")

// DB is a bbolt file.
type DB struct {
	db *bbolt.DB
}

// Open opens or creates the file at path, waiting at most timeout for its lock.
func Open(path string, timeout time.Duration) (*DB, error) {
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: timeout})
	if errors.Is(err, bbolterrors.ErrTimeout) {
		return nil, fmt.Errorf("%w: %w", kv.ErrLocked, err)
	}
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucket)
		return err
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db}, nil
}

func (d *DB) View(fn func(kv.Tx) error) error {
	return d.db.View(func(tx *bbolt.Tx) error { return fn(txn{tx.Bucket(bucket)}) })
}

func (d *DB) Update(fn func(kv.Tx) error) error {
	return d.db.Update(func(tx *bbolt.Tx) error { return fn(txn{tx.Bucket(bucket)}) })
}

func (d *DB) Close() error { return d.db.Close() }

type txn struct {
	b *bbolt.Bucket
}

func (t txn) Get(k string) []byte { return t.b.Get([]byte(k)) }

func (t txn) Put(k string, v []byte) error { return t.b.Put([]byte(k), v) }

func (t txn) Delete(k string) error { return t.b.Delete([]byte(k)) }

func (t txn) Scan(prefix string, fn func(k string, v []byte) error) error {
	p := []byte(prefix)
	cur := t.b.Cursor()
	for k, v := cur.Seek(p); k != nil && bytes.HasPrefix(k, p); k, v = cur.Next() {
		if err := fn(string(k), v); err != nil {
			return err
		}
	}
	return nil
}
