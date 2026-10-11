// Package kv stores flat string keys and byte values in transactions (ADR 51).
package kv

import (
	"errors"
	"time"
)

// DB is a store of keys, read and written in transactions.
type DB interface {
	View(fn func(Tx) error) error
	// Update commits when fn returns nil and rolls back otherwise.
	Update(fn func(Tx) error) error
	Close() error
}

// Tx is one transaction. Values it returns are valid only until it ends.
type Tx interface {
	// Get returns nil when k is absent.
	Get(k string) []byte
	Put(k string, v []byte) error
	Delete(k string) error
	// Scan visits keys beginning with prefix in order, stopping at fn's first
	// error. fn must not write.
	Scan(prefix string, fn func(k string, v []byte) error) error
}

// ErrLocked means another process holds the store.
var ErrLocked = errors.New("kv: held by another process")

// DeleteUnder deletes the keys beginning with prefix whose key and value match
// accepts, all of them when match is nil.
func DeleteUnder(tx Tx, prefix string, match func(k string, v []byte) bool) error {
	var keys []string
	err := tx.Scan(prefix, func(k string, v []byte) error {
		if match == nil || match(k, v) {
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

// DeleteExpired removes records whose owner-provided expiry is at or before now.
// A record whose expiry cannot be read is left for its owner to handle.
func DeleteExpired(tx Tx, prefix string, now time.Time, expires func(k string, v []byte) (time.Time, bool)) error {
	return DeleteUnder(tx, prefix, func(k string, v []byte) bool {
		at, ok := expires(k, v)
		return ok && !at.After(now)
	})
}
