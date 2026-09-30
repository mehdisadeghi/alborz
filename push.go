package alborz

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"git.mehdix.org/alborz/internal/keyring"
	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/record"
	"git.mehdix.org/alborz/internal/seal"
	"git.mehdix.org/alborz/internal/webpush"
)

// The push records (ADR 74): the instance's VAPID key and each account's devices.
const (
	pushOwner  = "alborz.push"
	pushFormat = 1
	vapidKey   = pushOwner + "/vapid"
)

// PushDevice is one browser that asked to be notified for one account.
type PushDevice struct {
	Endpoint     string
	P256dh, Auth []byte
	// Session turned it on; a push goes only while that session holds the account.
	Session string
	// Device is the browser's User-Agent; Lang and Zone are the page's that turned it on, for the words
	// and times a push shows; Origin is where it was turned on, the VAPID contact without -public-url.
	Device, Lang, Zone, Origin string
	Created                    time.Time
}

// Subscription is what webpush sends to.
func (d PushDevice) Subscription() webpush.Subscription {
	return webpush.Subscription{Endpoint: d.Endpoint, P256dh: d.P256dh, Auth: d.Auth}
}

// Pushes keeps the VAPID key and what was sent under the instance's push key, and the devices under the
// key the ring gives their account (ADR 72).
type Pushes struct {
	db    kv.DB
	key   *seal.Key
	keys  *keyring.Ring
	names seal.Names
	// Key signs every push; browsers subscribed to it accept no other.
	Key *webpush.Key
}

// pushSentKeep is how long a push is remembered as sent. A second send of one item comes from rounds or
// watchers overlapping by a tick, or a restart; two days is far past either.
const pushSentKeep = 48 * time.Hour

// openPushes reads the VAPID key, making it on the first start; one that does not open refuses the start
// rather than being made again, which would silence every browser subscribed to it.
func openPushes(db kv.DB, root seal.Root, keys *keyring.Ring, path string) (*Pushes, error) {
	p := &Pushes{db: db, key: root.Key(pushOwner), keys: keys, names: root.Names()}
	err := db.Update(func(tx kv.Tx) error {
		if sealed := tx.Get(vapidKey); sealed != nil {
			raw, err := p.key.Open(sealed)
			if err != nil {
				return fmt.Errorf("%s: the push key does not open: %w", path, err)
			}
			p.Key, err = webpush.ParseKey(raw)
			if err != nil {
				return fmt.Errorf("%s: the push key does not read: %w", path, err)
			}
			return nil
		}
		k, err := webpush.NewKey()
		if err != nil {
			return err
		}
		p.Key = k
		return tx.Put(vapidKey, p.key.Seal(k.Bytes()))
	})
	return p, err
}

// ID names a device without its endpoint, for a form to point at.
func (p *Pushes) ID(d PushDevice) string { return p.names.Of(d.Endpoint) }

// Topic names the account's pushes of one kind to the push service without naming the account.
func (p *Pushes) Topic(account, kind string) string { return p.names.Of(account + "\x00" + kind) }

func (p *Pushes) prefix(account string) string {
	return pushOwner + "/devices/" + p.keys.Name(account) + "/"
}

// open reads a device of the account; one no key at hand opens reads as absent.
func (p *Pushes) open(account string, sealed []byte, d *PushDevice) bool {
	raw, err := p.keys.Open(p.keys.Name(account), pushOwner, sealed)
	return err == nil && json.Unmarshal(raw, d) == nil
}

// put keeps d for the account, keeping what a newer version put in the record it replaces (ADR 26).
func (p *Pushes) put(tx kv.Tx, account string, d PushDevice) error {
	k, err := p.keys.For(p.keys.Name(account), pushOwner)
	if err != nil {
		return err
	}
	at := p.prefix(account) + p.ID(d)
	var old []byte
	if sealed := tx.Get(at); sealed != nil {
		if old, err = p.keys.Open(p.keys.Name(account), pushOwner, sealed); err != nil {
			return err
		}
	}
	raw, err := record.Keep(old, d)
	if err != nil {
		return err
	}
	return tx.Put(at, k.Seal(raw))
}

// Devices lists the account's devices, newest first.
func (p *Pushes) Devices(account string) ([]PushDevice, error) {
	var out []PushDevice
	err := p.db.View(func(tx kv.Tx) error {
		return tx.Scan(p.prefix(account), func(_ string, sealed []byte) error {
			var d PushDevice
			if p.open(account, sealed, &d) {
				out = append(out, d)
			}
			return nil
		})
	})
	slices.SortFunc(out, func(a, b PushDevice) int { return b.Created.Compare(a.Created) })
	return out, err
}

// Put keeps d for the account, in place of the record of the same endpoint.
func (p *Pushes) Put(account string, d PushDevice) error {
	return p.db.Update(func(tx kv.Tx) error { return p.put(tx, account, d) })
}

// Renew puts sub in place of the account's device at old, which a browser's push service replaced; an
// account with no device there is left as it is.
func (p *Pushes) Renew(account, old, session string, sub webpush.Subscription) error {
	return p.db.Update(func(tx kv.Tx) error {
		was := p.prefix(account) + p.names.Of(old)
		var d PushDevice
		if !p.open(account, tx.Get(was), &d) {
			return nil
		}
		d.Endpoint, d.P256dh, d.Auth, d.Session = sub.Endpoint, sub.P256dh, sub.Auth, session
		if err := tx.Delete(was); err != nil {
			return err
		}
		return p.put(tx, account, d)
	})
}

// Remove deletes the account's device of that ID.
func (p *Pushes) Remove(account, id string) error {
	return p.db.Update(func(tx kv.Tx) error { return tx.Delete(p.prefix(account) + id) })
}

// SignedOut deletes the account's devices the browser of that session turned on: signing out ends them.
func (p *Pushes) SignedOut(account, session string) error {
	return p.db.Update(func(tx kv.Tx) error {
		return kv.DeleteUnder(tx, p.prefix(account), func(sealed []byte) bool {
			var d PushDevice
			return p.open(account, sealed, &d) && d.Session == session
		})
	})
}

// Claim notes that item is sent to the endpoint now and reports whether it was not before, so a device
// gets each item at most once (ADR 74). What was sent longer than pushSentKeep ago is let go.
func (p *Pushes) Claim(endpoint, item string, now time.Time) (bool, error) {
	sent := pushOwner + "/sent/" + p.names.Of(endpoint) + "/"
	first := false
	err := p.db.Update(func(tx kv.Tx) error {
		at := sent + p.names.Of(item)
		if tx.Get(at) != nil {
			return nil
		}
		first = true
		err := kv.DeleteUnder(tx, sent, func(sealed []byte) bool {
			var when time.Time
			raw, err := p.key.Open(sealed)
			return err == nil && when.UnmarshalBinary(raw) == nil && now.Sub(when) > pushSentKeep
		})
		if err != nil {
			return err
		}
		stamp, err := now.MarshalBinary()
		if err != nil {
			return err
		}
		return tx.Put(at, p.key.Seal(stamp))
	})
	return first, err
}

// reseal moves the account's devices under the key the ring gives it now.
func (p *Pushes) reseal(account string) error {
	name := p.keys.Name(account)
	return p.db.Update(func(tx kv.Tx) error {
		var stale []PushDevice
		err := tx.Scan(p.prefix(account), func(_ string, sealed []byte) error {
			var d PushDevice
			if p.keys.Stale(name, pushOwner, sealed) && p.open(account, sealed, &d) {
				stale = append(stale, d)
			}
			return nil
		})
		for _, d := range stale {
			if err == nil {
				err = p.put(tx, account, d)
			}
		}
		return err
	})
}

// drop deletes the account's devices under an own key that is lost.
func (p *Pushes) drop(account string) error {
	name := p.keys.Name(account)
	return p.db.Update(func(tx kv.Tx) error {
		return kv.DeleteUnder(tx, p.prefix(account), func(sealed []byte) bool {
			_, err := p.keys.Open(name, pushOwner, sealed)
			return errors.Is(err, keyring.ErrLocked)
		})
	})
}
