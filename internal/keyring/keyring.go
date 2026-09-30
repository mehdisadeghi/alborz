// Package keyring holds each account's own key while a login of the account is alive, and the key pairs
// collections are shared to (ADR 72).
package keyring

import (
	"crypto/hpke"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/seal"
)

// Owner prefixes the key pairs in the data file (ADR 51); Format is theirs (ADR 26).
const (
	Owner  = "alborz.keys"
	Format = 1
)

// ErrLocked is a value under an account's own key while no key at hand opens it.
var ErrLocked = errors.New("keyring: the account's own key is not open")

// The suite of every wrap: a post-quantum hybrid KEM, since a copy of the file may be kept for decades.
var (
	kem  = hpke.MLKEM768X25519()
	kdf  = hpke.HKDFSHA256()
	aead = hpke.AES256GCM()
)

// Wrap is a secret sealed to an account's public key; To names that key, so a wrap made to a replaced
// pair is known as stale.
type Wrap struct {
	To  string
	Key []byte
}

type pairRecord struct {
	Public []byte
	// Private is sealed under the instance's key, or under the account's own while its encryption is on.
	Private []byte
	// Retired are pairs this one replaced, sealed as Private is, until every wrap to them is made again.
	Retired []retiredPair `json:",omitempty"`
}

type retiredPair struct {
	ID      string
	Private []byte
}

// Ring is every account's key pair and, while held, its own key. Keys are the accounts' keyed names.
type Ring struct {
	db    kv.DB
	root  seal.Root
	names seal.Names

	// writing orders pair writes; mu is never held across the data file, which a sealed store reads
	// the ring from inside its own transaction.
	writing  sync.Mutex
	mu       sync.Mutex
	accounts map[string]*account
	instance map[string]*seal.Key
}

type account struct {
	public  hpke.PublicKey
	id      string
	private []byte
	opened  hpke.PrivateKey
	retired []retiredPair
	// own is the account's own key while a login or a lease holds it; keys are derived from it. old are
	// keys it replaced, held until what they sealed is sealed again.
	own   *[seal.KeySize]byte
	old   [][seal.KeySize]byte
	keys  map[string]*seal.Key
	on    bool
	holds int
	until time.Time
}

// New reads every key pair in db.
func New(db kv.DB, root seal.Root) (*Ring, error) {
	r := &Ring{db: db, root: root, names: root.Names(), accounts: map[string]*account{}, instance: map[string]*seal.Key{}}
	err := db.View(func(tx kv.Tx) error {
		return tx.Scan(Owner+"/", func(k string, raw []byte) error {
			var rec pairRecord
			if err := json.Unmarshal(raw, &rec); err != nil {
				return err
			}
			a := &account{}
			if err := a.take(rec); err != nil {
				return err
			}
			r.accounts[strings.TrimPrefix(k, Owner+"/")] = a
			return nil
		})
	})
	return r, err
}

func idOf(public []byte) string {
	sum := sha256.Sum256(public)
	return hex.EncodeToString(sum[:8])
}

func (a *account) take(rec pairRecord) error {
	pub, err := kem.NewPublicKey(rec.Public)
	if err != nil {
		return err
	}
	a.public, a.id, a.private, a.retired = pub, idOf(rec.Public), rec.Private, rec.Retired
	a.on = seal.Format(rec.Private) == seal.ByAccount
	return nil
}

func (a *account) record() pairRecord {
	return pairRecord{Public: a.public.Bytes(), Private: a.private, Retired: a.retired}
}

// Name is the account's keyed name, as every record names it.
func (r *Ring) Name(address string) string { return r.names.Of(strings.ToLower(address)) }

// The caller holds mu.
func (r *Ring) get(name string) *account {
	a := r.accounts[name]
	if a == nil {
		a = &account{}
		r.accounts[name] = a
	}
	r.expire(a)
	return a
}

// expire forgets the own keys once no login or lease holds them; the caller holds mu.
func (r *Ring) expire(a *account) {
	if a.holds > 0 || time.Now().Before(a.until) {
		return
	}
	a.own, a.old, a.keys = nil, nil, nil
	if seal.Format(a.private) == seal.ByAccount {
		a.opened = nil
	}
	a.on = seal.Format(a.private) == seal.ByAccount
}

func (r *Ring) instanceKey(purpose string) *seal.Key {
	k := r.instance[purpose]
	if k == nil {
		k = r.root.Key(purpose)
		r.instance[purpose] = k
	}
	return k
}

// The caller holds mu.
func (a *account) key(purpose string) *seal.Key {
	k := a.keys[purpose]
	if k == nil {
		k = seal.AccountRoot(*a.own).Key(purpose)
		if a.keys == nil {
			a.keys = map[string]*seal.Key{}
		}
		a.keys[purpose] = k
	}
	return k
}

// open opens a value of the account's for purpose under any key at hand; the caller holds mu.
func (r *Ring) open(a *account, purpose string, sealed []byte) ([]byte, error) {
	if seal.Format(sealed) != seal.ByAccount {
		return r.instanceKey(purpose).Open(sealed)
	}
	if a.own != nil {
		if plain, err := a.key(purpose).Open(sealed); err == nil {
			return plain, nil
		}
	}
	for _, old := range a.old {
		if plain, err := seal.AccountRoot(old).Key(purpose).Open(sealed); err == nil {
			return plain, nil
		}
	}
	return nil, ErrLocked
}

func (r *Ring) parse(a *account, sealed []byte) hpke.PrivateKey {
	raw, err := r.open(a, Owner, sealed)
	if err != nil {
		return nil
	}
	priv, err := kem.NewPrivateKey(raw)
	if err != nil {
		return nil
	}
	return priv
}

// privateKey opens the account's private key where a key at hand opens it; the caller holds mu.
func (r *Ring) privateKey(a *account) hpke.PrivateKey {
	if a.opened == nil && a.public != nil {
		a.opened = r.parse(a, a.private)
	}
	return a.opened
}

// Hold keeps the account's own key, nil for none, until release is called, and turns its encryption on
// or off: new values go under the key the ring gives them. An account whose encryption is on but whose
// pair no key at hand opens is locked.
func (r *Ring) Hold(name string, own []byte, on bool) (release func(), err error) {
	if err := r.turn(name, own, on, true); err != nil {
		return nil, err
	}
	return sync.OnceFunc(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		a := r.accounts[name]
		a.holds--
		r.expire(a)
	}), nil
}

// Turn is Hold for an account already held.
func (r *Ring) Turn(name string, own []byte, on bool) error { return r.turn(name, own, on, false) }

func (r *Ring) turn(name string, own []byte, on, hold bool) error {
	r.writing.Lock()
	defer r.writing.Unlock()
	r.mu.Lock()
	a := r.get(name)
	if own != nil {
		if a.own != nil && *a.own != [seal.KeySize]byte(own) {
			a.old = append(a.old, *a.own)
		}
		k := [seal.KeySize]byte(own)
		a.own, a.keys = &k, nil
	}
	if hold {
		a.holds++
	}
	a.on = on
	changed, err := r.settle(a)
	var rec pairRecord
	if changed {
		rec = a.record()
	}
	r.mu.Unlock()
	if err != nil || !changed {
		return err
	}
	return r.write(name, rec)
}

// settle makes the account's pair, or seals its private key under the key its encryption asks for. A pair
// going under an own key it was not under is replaced, so no copy made before opens what follows; the
// old one is kept as retired until the wraps to it are made again. The caller holds mu.
func (r *Ring) settle(a *account) (bool, error) {
	target := r.instanceKey(Owner)
	if a.on {
		if a.own == nil {
			return false, nil
		}
		target = a.key(Owner)
	}
	if a.public != nil {
		if _, err := target.Open(a.private); err == nil {
			return false, nil
		}
	}
	priv := r.privateKey(a)
	if priv == nil && a.public != nil {
		return false, nil
	}
	retired := a.retired
	for i, old := range retired {
		if raw := r.parse(a, old.Private); raw != nil {
			b, err := raw.Bytes()
			if err != nil {
				return false, err
			}
			retired[i].Private = target.Seal(b)
		}
	}
	if priv != nil && a.on {
		b, err := priv.Bytes()
		if err != nil {
			return false, err
		}
		retired = append(retired, retiredPair{ID: a.id, Private: target.Seal(b)})
		priv = nil
	}
	if priv == nil {
		var err error
		if priv, err = kem.GenerateKey(); err != nil {
			return false, err
		}
	}
	raw, err := priv.Bytes()
	if err != nil {
		return false, err
	}
	if err := a.take(pairRecord{Public: priv.PublicKey().Bytes(), Private: target.Seal(raw), Retired: retired}); err != nil {
		return false, err
	}
	a.opened, a.on = priv, target.Format() == seal.ByAccount
	return true, nil
}

func (r *Ring) write(name string, rec pairRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return r.db.Update(func(tx kv.Tx) error { return tx.Put(Owner+"/"+name, raw) })
}

// Retire holds keys the account's own replaced, for opening what they sealed until it is sealed again.
func (r *Ring) Retire(name string, keys ...[]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.get(name)
	for _, k := range keys {
		a.old = append(a.old, [seal.KeySize]byte(k))
	}
	a.opened = nil
}

// Settled forgets the retired keys and pairs once everything they opened is sealed again.
func (r *Ring) Settled(name string) error {
	r.writing.Lock()
	defer r.writing.Unlock()
	r.mu.Lock()
	a := r.get(name)
	a.old = nil
	had := len(a.retired) > 0
	a.retired = nil
	var rec pairRecord
	if had {
		rec = a.record()
	}
	r.mu.Unlock()
	if !had {
		return nil
	}
	return r.write(name, rec)
}

// Replace gives the account a new pair under own, nil for the instance's key, dropping the old: what it
// opened is lost to the account. It is the reader's start over, when the key that sealed it is lost.
func (r *Ring) Replace(name string, own []byte) error {
	r.writing.Lock()
	defer r.writing.Unlock()
	r.mu.Lock()
	a := r.get(name)
	a.public, a.opened, a.retired, a.own, a.old, a.keys, a.on = nil, nil, nil, nil, nil, nil, own != nil
	if own != nil {
		k := [seal.KeySize]byte(own)
		a.own = &k
	}
	_, err := r.settle(a)
	rec := a.record()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	return r.write(name, rec)
}

// Lease holds the own key for d past its last login, for a client that signs in on every request.
func (r *Ring) Lease(name string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.get(name); a.own != nil {
		a.until = time.Now().Add(d)
	}
}

// On reports whether the account's new values go under its own key.
func (r *Ring) On(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.get(name).on
}

// The caller holds mu.
func (r *Ring) locked(a *account) bool {
	return a.on && (a.own == nil || a.public != nil && r.privateKey(a) == nil)
}

// Locked reports an account whose encryption is on and whose pair no key at hand opens: its work waits.
func (r *Ring) Locked(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.locked(r.get(name))
}

// Own is the account's own key while held.
func (r *Ring) Own(name string) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.get(name)
	if a.own == nil {
		return nil, false
	}
	return a.own[:], true
}

// For is the key the account's new values for purpose go under.
func (r *Ring) For(name, purpose string) (*seal.Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.get(name)
	switch {
	case !a.on:
		return r.instanceKey(purpose), nil
	case r.locked(a):
		return nil, ErrLocked
	}
	return a.key(purpose), nil
}

// Open opens a value of the account's for purpose under whichever key at hand sealed it.
func (r *Ring) Open(name, purpose string, sealed []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.open(r.get(name), purpose, sealed)
}

// Stale reports a value not under the key the account's new values for purpose go under.
func (r *Ring) Stale(name, purpose string, sealed []byte) bool {
	k, err := r.For(name, purpose)
	if err != nil {
		return false
	}
	if seal.Format(sealed) != k.Format() {
		return true
	}
	_, err = k.Open(sealed)
	return err != nil
}

// ID names the account's public key, "" before it has a pair.
func (r *Ring) ID(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.get(name).id
}

// Wrap seals secret to the account's public key, bound to info; false before it has a pair.
func (r *Ring) Wrap(name string, secret []byte, info string) (Wrap, bool, error) {
	r.mu.Lock()
	a := r.get(name)
	pub, id := a.public, a.id
	r.mu.Unlock()
	if pub == nil {
		return Wrap{}, false, nil
	}
	sealed, err := hpke.Seal(pub, kdf, aead, []byte(info), secret)
	return Wrap{To: id, Key: sealed}, err == nil, err
}

// Unwrap opens the first of wraps, by the keyed names of their accounts, whose private key is at hand: the
// account's pair, or one it retired.
func (r *Ring) Unwrap(wraps map[string]Wrap, info string) ([]byte, bool) {
	for name, w := range wraps {
		r.mu.Lock()
		a := r.get(name)
		var priv hpke.PrivateKey
		if w.To == a.id {
			priv = r.privateKey(a)
		}
		for _, old := range a.retired {
			if priv == nil && w.To == old.ID {
				priv = r.parse(a, old.Private)
			}
		}
		r.mu.Unlock()
		if priv == nil {
			continue
		}
		if secret, err := hpke.Open(priv, kdf, aead, []byte(info), w.Key); err == nil {
			return secret, true
		}
	}
	return nil, false
}
