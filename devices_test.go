package alborz

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz/internal/keyring"
	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/internal/record"
	"git.mehdix.org/alborz/internal/seal"
)

// The record as the version before ADR 109 knew it.
type deviceRecordBefore struct {
	Only    bool           `json:",omitempty"`
	Devices []deviceBefore `json:",omitempty"`
}

type deviceBefore struct {
	ID       string
	Name     string
	Created  time.Time
	LastUsed time.Time `json:",omitzero"`
	ReadOnly bool      `json:",omitempty"`
	Hash     []byte
	KeyID    string `json:",omitempty"`
	ByDevice []byte `json:",omitempty"`
}

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

// A record kept before device passwords had scopes reads as passwords for every collection, and a write
// leaves it in the shape it had (ADR 26).
func TestADeviceRecordFromBeforeScopesReadsUnscoped(t *testing.T) {
	const account = "a@example.org"
	d := devicesIn(t)
	d.stored(t, account, func([]byte) []byte {
		before := deviceRecordBefore{Devices: make([]deviceBefore, 1)}
		before.Devices[0] = deviceBefore{ID: "1", Name: "phone", Created: time.Now().UTC(), ReadOnly: true, Hash: deviceHash("test")}
		raw, err := json.Marshal(before)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	})
	p, _, err := d.match(account, "test")
	if err != nil || p == nil || p.Scoped || p.Scope != nil || !p.ReadOnly {
		t.Fatalf("the password of an older record reads as %+v: %v", p, err)
	}
	if err := d.touch(account, "1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if raw := d.stored(t, account, nil); strings.Contains(string(raw), "Scope") || !strings.Contains(string(raw), `"LastUsed"`) {
		t.Fatalf("the record was rewritten as %s", raw)
	}
}

// A version from before scopes does not read the scoped passwords, so it refuses them instead of taking
// them for every collection, and its writes keep them with their scopes.
func TestAnOlderVersionRefusesAScopedDevicePasswordAndKeepsIt(t *testing.T) {
	const account = "a@example.org"
	d := devicesIn(t)
	scope := []DeviceCollection{{Kind: "calendar", Ref: "plans#7"}}
	err := d.update(account, func(rec *deviceRecord) bool {
		rec.Devices = append(rec.Devices,
			DevicePassword{ID: "1", Name: "phone", Hash: deviceHash("whole")},
			DevicePassword{ID: "2", Name: "wall", Hash: deviceHash("limited"), ReadOnly: true, Scoped: true, Scope: scope})
		return true
	})
	if err != nil {
		t.Fatal(err)
	}

	d.stored(t, account, func(raw []byte) []byte {
		var before deviceRecordBefore
		if err := json.Unmarshal(raw, &before); err != nil {
			t.Fatal(err)
		}
		if len(before.Devices) != 1 || before.Devices[0].Name != "phone" {
			t.Fatalf("an older version reads %+v as the account's device passwords", before.Devices)
		}
		before.Devices[0].LastUsed = time.Now().UTC()
		kept, err := record.Keep(raw, &before)
		if err != nil {
			t.Fatal(err)
		}
		return kept
	})

	p, _, err := d.match(account, "limited")
	if err != nil || p == nil || !p.Scoped || !p.ReadOnly || len(p.Scope) != 1 || p.Scope[0] != scope[0] {
		t.Fatalf("after an older version's write the scoped password reads as %+v: %v", p, err)
	}
	if p, _, err := d.match(account, "whole"); err != nil || p == nil || p.Scoped || p.LastUsed.IsZero() {
		t.Fatalf("after an older version's write the other password reads as %+v: %v", p, err)
	}
	if found, err := d.Revoke(account, "2"); err != nil || !found {
		t.Fatalf("revoking the scoped password: %v, %v", found, err)
	}
	if raw := d.stored(t, account, nil); strings.Contains(string(raw), "Scoped") {
		t.Fatalf("revoked, the record still holds %s", raw)
	}
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
