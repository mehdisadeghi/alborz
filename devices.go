package alborz

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz/internal/keyring"
	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/record"
	"git.mehdix.org/alborz/internal/seal"
)

// Device passwords (ADR 107): one record per account at alborz.devices/<keyed account>, sealed under the
// instance's key, since a DAV request reads it before anything opens the account's own key.
const (
	devicesOwner  = "alborz.devices"
	devicesFormat = 1
	// deviceTouch is how stale a device password's last use may be before a request writes it again.
	deviceTouch = time.Hour
)

// DevicePassword is a password a calendar and contacts client signs in with in place of the mail password.
// Only its hash is kept.
type DevicePassword struct {
	ID       string
	Name     string
	Created  time.Time
	LastUsed time.Time `json:",omitzero"`
	ReadOnly bool      `json:",omitempty"`
	Hash     []byte
	// KeyID names the account's own key ByDevice wraps under the password; both are empty while the
	// account's encryption was off.
	KeyID    string `json:",omitempty"`
	ByDevice []byte `json:",omitempty"`
}

type deviceRecord struct {
	// Only refuses the mail password to DAV clients.
	Only    bool             `json:",omitempty"`
	Devices []DevicePassword `json:",omitempty"`
}

// Devices keeps the accounts' device passwords.
type Devices struct {
	db   kv.DB
	key  *seal.Key
	keys *keyring.Ring
}

// ErrNoDevicePassword is a password that is none of the account's device passwords, while its mail password
// is still taken.
var ErrNoDevicePassword = errors.New("alborz: not a device password of the account")

func openDevices(db kv.DB, root seal.Root, keys *keyring.Ring) *Devices {
	return &Devices{db: db, key: root.Key(devicesOwner), keys: keys}
}

func (d *Devices) at(account string) string { return devicesOwner + "/" + d.keys.Name(account) }

// read is the account's record; one that does not open fails, so nothing is written over it (ADR 59).
func (d *Devices) read(tx kv.Tx, account string) (deviceRecord, []byte, error) {
	var rec deviceRecord
	sealed := tx.Get(d.at(account))
	if sealed == nil {
		return rec, nil, nil
	}
	raw, err := d.key.Open(sealed)
	if err != nil {
		return rec, nil, err
	}
	return rec, raw, json.Unmarshal(raw, &rec)
}

// update changes the account's record where change says it changed, keeping what a newer version put there.
func (d *Devices) update(account string, change func(*deviceRecord) bool) error {
	return d.db.Update(func(tx kv.Tx) error {
		rec, old, err := d.read(tx, account)
		if err != nil || !change(&rec) {
			return err
		}
		if !rec.Only && len(rec.Devices) == 0 {
			return tx.Delete(d.at(account))
		}
		raw, err := record.Keep(old, &rec)
		if err != nil {
			return err
		}
		return tx.Put(d.at(account), d.key.Seal(raw))
	})
}

func (d *Devices) view(account string) (deviceRecord, error) {
	var rec deviceRecord
	err := d.db.View(func(tx kv.Tx) (err error) {
		rec, _, err = d.read(tx, account)
		return err
	})
	return rec, err
}

// List is the account's device passwords, newest first, and whether DAV clients take device passwords only.
func (d *Devices) List(account string) ([]DevicePassword, bool, error) {
	rec, err := d.view(account)
	slices.SortFunc(rec.Devices, func(a, b DevicePassword) int { return b.Created.Compare(a.Created) })
	return rec.Devices, rec.Only, err
}

// SetOnly turns the refusal of the mail password to DAV clients on or off.
func (d *Devices) SetOnly(account string, only bool) error {
	return d.update(account, func(rec *deviceRecord) bool {
		changed := rec.Only != only
		rec.Only = only
		return changed
	})
}

// Revoke deletes the account's device password of that ID and reports whether there was one.
func (d *Devices) Revoke(account, id string) (bool, error) {
	found := false
	err := d.update(account, func(rec *deviceRecord) bool {
		n := len(rec.Devices)
		rec.Devices = slices.DeleteFunc(rec.Devices, func(p DevicePassword) bool { return p.ID == id })
		found = len(rec.Devices) != n
		return found
	})
	return found, err
}

func deviceHash(password string) []byte {
	sum := sha256.Sum256([]byte(password))
	return sum[:]
}

// deviceKey is the key a device password derives; rand.Text's 130 random bits need no stretching.
func deviceKey(password string) *seal.Key {
	raw, err := hkdf.Key(sha256.New, []byte(password), nil, "alborz device password", seal.KeySize)
	if err != nil {
		panic(err)
	}
	return seal.NewKey([seal.KeySize]byte(raw))
}

// match finds the account's device password that password is, comparing every hash in constant time.
func (d *Devices) match(account, password string) (*DevicePassword, bool, error) {
	rec, err := d.view(account)
	if err != nil {
		return nil, false, err
	}
	hash := deviceHash(password)
	var found *DevicePassword
	for i := range rec.Devices {
		if subtle.ConstantTimeCompare(rec.Devices[i].Hash, hash) == 1 {
			found = &rec.Devices[i]
		}
	}
	return found, rec.Only, nil
}

// touch notes a device password's use, at most once per deviceTouch.
func (d *Devices) touch(account, id string, now time.Time) error {
	return d.update(account, func(rec *deviceRecord) bool {
		for i, p := range rec.Devices {
			if p.ID == id && now.Sub(p.LastUsed) >= deviceTouch {
				rec.Devices[i].LastUsed = now
				return true
			}
		}
		return false
	})
}

// prune deletes the account's device passwords that do not wrap its own key as the ring holds it now: turning
// encryption on or off or starting over leaves them with a key that is gone, and their secrets are not kept
// to wrap the new one. An account whose key is not open keeps them.
func (d *Devices) prune(account string) error {
	name, want := d.keys.Name(account), ""
	if d.keys.On(name) {
		own, ok := d.keys.Own(name)
		if !ok {
			return nil
		}
		want = keyID(own)
	}
	return d.update(account, func(rec *deviceRecord) bool {
		n := len(rec.Devices)
		rec.Devices = slices.DeleteFunc(rec.Devices, func(p DevicePassword) bool { return p.KeyID != want })
		return len(rec.Devices) != n
	})
}

// CreateDevicePassword makes a device password for the login's account and returns it, which nothing keeps:
// the reader copies it into the client. Under encryption it wraps the account's own key, as the recovery key
// does, so a client signing in with it opens the account (ADR 72).
func (s *Server) CreateDevicePassword(l *Login, name string, readOnly bool) (string, error) {
	password := rand.Text()
	id := make([]byte, 8)
	rand.Read(id)
	p := DevicePassword{ID: hex.EncodeToString(id), Name: name, Created: time.Now().UTC(), ReadOnly: readOnly, Hash: deviceHash(password)}
	keyed := s.Keys.Name(l.username)
	if s.Keys.On(keyed) {
		own, ok := s.Keys.Own(keyed)
		if !ok || s.Keys.Locked(keyed) {
			return "", ErrLocked
		}
		p.KeyID, p.ByDevice = keyID(own), deviceKey(password).Seal(own)
	}
	err := s.Devices.update(l.username, func(rec *deviceRecord) bool {
		rec.Devices = append(rec.Devices, p)
		return true
	})
	return password, err
}

// CheckDevicePassword checks password against the account's device passwords, sharing the sign-in tries
// (ADR 30). A match holds the account's own key for lease, opening it with the password where no login
// holds it. No match is ErrNoDevicePassword, or an AuthError where the account takes device passwords only.
func (s *Server) CheckDevicePassword(username, password, from string, lease time.Duration) (DevicePassword, error) {
	username = strings.ToLower(username)
	var found DevicePassword
	err := s.signInRefused.attempt(readerKey(from, username), func() error {
		p, only, err := s.Devices.match(username, password)
		switch {
		case err != nil:
			return err
		case p != nil:
			found = *p
			return nil
		case only:
			return AuthError{ErrNoDevicePassword}
		}
		return ErrNoDevicePassword
	})
	if err != nil {
		return found, err
	}
	if err := s.Devices.touch(username, found.ID, time.Now().UTC()); err != nil {
		s.log.Printf("%s: noting a device password's use: %v", username, err)
	}
	keyed := s.Keys.Name(username)
	if !s.Keys.Locked(keyed) || found.ByDevice == nil {
		s.Keys.Lease(keyed, lease)
		return found, nil
	}
	own, err := deviceKey(password).Open(found.ByDevice)
	if err != nil || len(own) != seal.KeySize || keyID(own) != found.KeyID {
		s.log.Printf("%s: a device password does not open the account's key", username)
		return found, nil
	}
	if err := s.Keys.Lend(keyed, own, lease); err != nil {
		s.log.Printf("%s: opening the account's key with a device password: %v", username, err)
	}
	return found, nil
}
