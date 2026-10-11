package bolt_test

import (
	"path/filepath"
	"testing"
	"time"

	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/kv/bolt"
)

func TestExpiredRecordsAreRemovedFromBolt(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "data.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now()
	err = db.Update(func(tx kv.Tx) error {
		if err := tx.Put("claims/old", []byte("old")); err != nil {
			return err
		}
		return tx.Put("claims/live", []byte("live"))
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx kv.Tx) error {
		return kv.DeleteExpired(tx, "claims/", now, func(k string, _ []byte) (time.Time, bool) {
			if k == "claims/old" {
				return now.Add(-time.Second), true
			}
			return now.Add(time.Second), true
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.View(func(tx kv.Tx) error {
		if tx.Get("claims/old") != nil || string(tx.Get("claims/live")) != "live" {
			t.Fatalf("expired claim remained or live claim was removed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
