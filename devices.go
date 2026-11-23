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
	// KeyID names the account's own key the password was made under, and ByDevice wraps it under the
	// password; both are empty while the account's encryption was off. A scoped password has no ByDevice
	// but Wraps: the own keys of its collections that had one, each under the password, by Ref (ADR 109).
	KeyID    string            `json:",omitempty"`
	ByDevice []byte            `json:",omitempty"`
	Wraps    map[string][]byte `json:",omitempty"`
	// Scoped limits the password to the collections of Scope (ADR 109); the record's list that holds it
	// says so, not a field.
	Scoped bool               `json:"-"`
	Scope  []DeviceCollection `json:",omitempty"`
}

// DeviceCollection is one collection a device password is limited to.
type DeviceCollection struct {
	// Kind is what the pages call it: calendar, tasks, journal or addressbook.
	Kind string
	// Ref names the collection to the server of the DAV clients, which no page shows.
	Ref string
}

// DeviceChoice is a collection a new device password can be limited to, as the account sees it: its
// address to a DAV client and its ID there, its name, its other owner and whether a feed fills it.
type DeviceChoice struct {
	DeviceCollection
	Path, ID, Name, SharedBy string
	Subscribed               bool
}

type deviceRecord struct {
	// Only refuses the mail password to DAV clients.
	Only    bool             `json:",omitempty"`
	Devices []DevicePassword `json:",omitempty"`
	// Scoped are the passwords limited to some collections as kept, apart from Devices: a version from
	// before ADR 109 does not read them, so it refuses them instead of taking them for every collection,
	// and keeps them through its writes (ADR 26). Read, they are among Devices.
	Scoped []DevicePassword `json:",omitempty"`
}

// kept is the record as written.
func (r deviceRecord) kept() *deviceRecord {
	out := deviceRecord{Only: r.Only}
	for _, p := range r.Devices {
		if p.Scoped {
			out.Scoped = append(out.Scoped, p)
		} else {
			out.Devices = append(out.Devices, p)
		}
	}
	return &out
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
	err = json.Unmarshal(raw, &rec)
	for _, p := range rec.Scoped {
		p.Scoped = true
		rec.Devices = append(rec.Devices, p)
	}
	rec.Scoped = nil
	return rec, raw, err
}

// update changes the account's record where change says it changed, keeping what a newer version put there.
func (d *Devices) update(account string, change func(*deviceRecord) bool) error {
	return d.db.Update(func(tx kv.Tx) error {
		rec, old, err := d.read(tx, account)
		if err != nil || !change(&rec) {
			return err
		}
		raw, err := record.Keep(old, rec.kept())
		if err != nil {
			return err
		}
		// Empty of what a newer version put there too.
		if string(raw) == "{}" {
			return tx.Delete(d.at(account))
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

// The HKDF info of the keys a device password derives: the one wrapping the account's own key and, followed
// by a collection's Ref, the one wrapping that collection's.
const (
	deviceInfo           = "alborz device password"
	deviceCollectionInfo = "alborz device password collection "
)

// deviceKey is the key a device password derives for info; rand.Text's 130 random bits need no stretching.
func deviceKey(password, info string) *seal.Key {
	raw, err := hkdf.Key(sha256.New, []byte(password), nil, info, seal.KeySize)
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

// prune deletes the account's device passwords that were not made under its own key as the ring holds it
// now: turning encryption on or off or starting over leaves them wrapping keys that are gone, and their
// secrets are not kept to wrap the new ones. An account whose key is not open keeps them.
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
	return s.createDevicePassword(l, DevicePassword{Name: name, ReadOnly: readOnly})
}

// CreateScopedDevicePassword makes one limited to the collections of scope (ADR 109). Under encryption it
// wraps the keys of those collections and not the account's own, so it opens no other collection.
func (s *Server) CreateScopedDevicePassword(l *Login, name string, readOnly bool, scope []DeviceCollection) (string, error) {
	return s.createDevicePassword(l, DevicePassword{Name: name, ReadOnly: readOnly, Scoped: true, Scope: scope})
}

func (s *Server) createDevicePassword(l *Login, p DevicePassword) (string, error) {
	password := rand.Text()
	id := make([]byte, 8)
	rand.Read(id)
	p.ID, p.Created, p.Hash = hex.EncodeToString(id), time.Now().UTC(), deviceHash(password)
	keyed := s.Keys.Name(l.username)
	if s.Keys.On(keyed) {
		own, ok := s.Keys.Own(keyed)
		if !ok || s.Keys.Locked(keyed) {
			return "", ErrLocked
		}
		p.KeyID = keyID(own)
		if p.Scoped {
			p.Wraps = map[string][]byte{}
		} else {
			p.ByDevice = deviceKey(password, deviceInfo).Seal(own)
		}
		for _, c := range p.Scope {
			key, err := s.CollectionKey(c.Ref)
			if err != nil {
				return "", err
			}
			if key != nil {
				p.Wraps[c.Ref] = deviceKey(password, deviceCollectionInfo+c.Ref).Seal(key)
			}
		}
	}
	err := s.Devices.update(l.username, func(rec *deviceRecord) bool {
		rec.Devices = append(rec.Devices, p)
		return true
	})
	return password, err
}

// Keys are the keys a scoped password made under encryption wraps, opened, by their collections' Ref: its
// requests are served with them alone (ADR 109). Any other password wraps none, which is nil.
func (p DevicePassword) Keys(password string) map[string][]byte {
	if !p.Scoped || p.KeyID == "" {
		return nil
	}
	keys := map[string][]byte{}
	for ref, wrap := range p.Wraps {
		if key, err := deviceKey(password, deviceCollectionInfo+ref).Open(wrap); err == nil {
			keys[ref] = key
		}
	}
	return keys
}

// CheckDevicePassword checks password against the account's device passwords, sharing the sign-in tries
// (ADR 30). A match that is not scoped holds the account's own key for lease, opening it with the password
// where no login holds it. No match is ErrNoDevicePassword, or an AuthError where the account takes device
// passwords only.
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
	switch {
	case found.Scoped:
		// Served with the keys it wraps, so it holds the account's own for no time at all.
		return found, nil
	case !s.Keys.Locked(keyed) || found.ByDevice == nil:
		s.Keys.Lease(keyed, lease)
		return found, nil
	}
	own, err := deviceKey(password, deviceInfo).Open(found.ByDevice)
	if err != nil || len(own) != seal.KeySize || keyID(own) != found.KeyID {
		s.log.Printf("%s: a device password does not open the account's key", username)
		return found, nil
	}
	if err := s.Keys.Lend(keyed, own, lease); err != nil {
		s.log.Printf("%s: opening the account's key with a device password: %v", username, err)
	}
	return found, nil
}
