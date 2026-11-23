package alborz

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz/internal/keyring"
	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/internal/seal"
)

func devicesIn(t *testing.T) *Devices {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), storeFile), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	root, err := seal.ParseRoot("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := keyring.New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	return &Devices{db: db, key: root.Key(devicesOwner), keys: keys}
}

// stored is the account's record as the file keeps it; rewrite replaces it as a version would.
func (d *Devices) stored(t *testing.T, account string, rewrite func(raw []byte) []byte) []byte {
	t.Helper()
	var raw []byte
	err := d.db.Update(func(tx kv.Tx) (err error) {
		if sealed := tx.Get(d.at(account)); sealed != nil {
			if raw, err = d.key.Open(sealed); err != nil {
				return err
			}
		}
		if rewrite == nil {
			return nil
		}
		raw = rewrite(raw)
		return tx.Put(d.at(account), d.key.Seal(raw))
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A record left with no device password is deleted, unless a newer version put something there (ADR 26).
func TestAnEmptiedDeviceRecordKeepsWhatANewerVersionPutThere(t *testing.T) {
	const account = "a@example.org"
	d := devicesIn(t)
	add := func(rec *deviceRecord) bool {
		rec.Devices = append(rec.Devices, DevicePassword{ID: "1", Name: "phone", Hash: deviceHash("test")})
		return true
	}
	if err := d.update(account, add); err != nil {
		t.Fatal(err)
	}
	if found, err := d.Revoke(account, "1"); err != nil || !found {
		t.Fatalf("revoking the only password: %v, %v", found, err)
	}
	if raw := d.stored(t, account, nil); raw != nil {
		t.Fatalf("emptied, the record is kept as %s", raw)
	}
	if err := d.update(account, add); err != nil {
		t.Fatal(err)
	}
	d.stored(t, account, func(raw []byte) []byte {
		return []byte(strings.Replace(string(raw), "{", `{"Later":[{"ID":"9"}],`, 1))
	})
	if found, err := d.Revoke(account, "1"); err != nil || !found {
		t.Fatalf("revoking the only password this version reads: %v, %v", found, err)
	}
	if raw := d.stored(t, account, nil); string(raw) != `{"Later":[{"ID":"9"}]}` {
		t.Fatalf("emptied of what this version reads, the record is %q", raw)
	}
}
