// Package kv is the one way alborz keeps data: flat string keys, byte
// values, and transactions; see ADR 51. An engine implements DB, and
// copying every key of one into another moves the data between them.
package kv

import "errors"

// DB is a store of keys, read and written in transactions.
type DB interface {
	// View runs fn in a transaction that only reads.
	View(fn func(Tx) error) error
	// Update runs fn in a transaction that is kept when fn returns nil,
	// and undone when it returns an error.
	Update(fn func(Tx) error) error
	Close() error
}

// Tx is one transaction. A value it hands out is valid until the
// transaction ends; one kept longer is copied.
type Tx interface {
	// Get is the value under k, nil when there is none.
	Get(k string) []byte
	Put(k string, v []byte) error
	Delete(k string) error
	// Scan calls fn for every key beginning with prefix, in key order,
	// and stops at the first error fn returns. fn does not write.
	Scan(prefix string, fn func(k string, v []byte) error) error
}

// ErrLocked is a store another process holds.
var ErrLocked = errors.New("kv: held by another process")

// DeleteUnder deletes the keys beginning with prefix whose value match
// takes, every one when match is nil.
func DeleteUnder(tx Tx, prefix string, match func(v []byte) bool) error {
	var keys []string
	err := tx.Scan(prefix, func(k string, v []byte) error {
		if match == nil || match(v) {
			keys = append(keys, k)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := tx.Delete(k); err != nil {
			return err
		}
	}
	return nil
}
