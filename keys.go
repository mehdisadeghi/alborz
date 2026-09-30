package alborz

import (
	"context"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"git.mehdix.org/alborz/internal/keyring"
	"git.mehdix.org/alborz/internal/seal"
)

// keyEntry keeps an account's own key in its settings (ADR 72).
const keyEntry = "key"

// accountKey is the account's own key, wrapped under a key derived from its mail password and under its
// recovery key. Each time encryption is turned on, or started over, a new key replaces it; the ones it
// replaced stay as Retired, so another alborz holding values under one of them seals them again.
type accountKey struct {
	// ID names the key without telling it, so a key held in memory is known as this one or a retired one.
	ID         string
	Salt       []byte
	ByPassword []byte
	ByRecovery []byte
	// Off is encryption turned off: new values go under the instance's keys.
	Off     bool
	Retired []accountKey `json:",omitempty"`
}

func keyID(own []byte) string {
	id, err := hkdf.Key(sha256.New, own, nil, "alborz key id", 8)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(id)
}

// renewed is k with a fresh key under password and a new recovery key, the one it replaced retired.
func (k accountKey) renewed(password string) (accountKey, []byte, string) {
	retired := k.Retired
	if k.made() {
		k.Retired, k.Off = nil, false
		retired = append(retired, k)
	}
	own := make([]byte, seal.KeySize)
	rand.Read(own)
	recovery := recoveryText()
	next := accountKey{ID: keyID(own), ByRecovery: recoveryKey(recovery).Seal(own), Retired: retired}
	return next.under(own, password), own, recovery
}

// retiredBy are the retired keys password opens.
func (k accountKey) retiredBy(password string) [][]byte {
	var out [][]byte
	for _, old := range k.Retired {
		if own := old.open(password); own != nil {
			out = append(out, own)
		}
	}
	return out
}

func (k accountKey) made() bool { return len(k.ByPassword) > 0 }

func (k accountKey) on() bool { return k.made() && !k.Off }

func (k accountKey) open(password string) []byte {
	own, err := passwordKey(password, k.Salt).Open(k.ByPassword)
	if err != nil || len(own) != seal.KeySize {
		return nil
	}
	return own
}

// under wraps own under password, with a fresh salt.
func (k accountKey) under(own []byte, password string) accountKey {
	k.Salt = make([]byte, envelopeSalt)
	rand.Read(k.Salt)
	k.ByPassword = passwordKey(password, k.Salt).Seal(own)
	return k
}

// recoveryKey is the key a recovery key text derives, read as the page shows it or typed without its
// dashes, in either case. rand.Text's 26 characters hold 130 random bits, so no stretching is needed.
func recoveryKey(text string) *seal.Key {
	var b strings.Builder
	for _, r := range strings.ToUpper(text) {
		if 'A' <= r && r <= 'Z' || '2' <= r && r <= '7' {
			b.WriteRune(r)
		}
	}
	raw, err := hkdf.Key(sha256.New, []byte(b.String()), nil, "alborz recovery", seal.KeySize)
	if err != nil {
		panic(err)
	}
	return seal.NewKey([seal.KeySize]byte(raw))
}

// recoveryText is a new recovery key, in groups of four for reading it out.
func recoveryText() string {
	text := rand.Text()
	var groups []string
	for len(text) > 4 {
		groups, text = append(groups, text[:4]), text[4:]
	}
	return strings.Join(append(groups, text), "-")
}

func keyOf(ctx context.Context, st Store) (accountKey, error) {
	var k accountKey
	err := st.Get(ctx, keyEntry, &k)
	if errors.Is(err, ErrNoStoreEntry) {
		return accountKey{}, nil
	}
	return k, err
}

// rewrapKey wraps the account's own key under next, the mail password replacing the login's.
func (s *Server) rewrapKey(ctx context.Context, l *Login, next string) error {
	k, err := keyOf(ctx, l.store)
	if err != nil || !k.made() {
		return err
	}
	own := k.open(l.password)
	if held, ok := s.Keys.Own(s.Keys.Name(l.username)); own == nil && ok {
		own = held
	}
	if own == nil {
		return nil
	}
	for i, old := range k.Retired {
		if was := old.open(l.password); was != nil {
			k.Retired[i] = old.under(was, next)
		}
	}
	k = k.under(own, next)
	return l.store.Put(ctx, keyEntry, &k)
}

// A Sealer is a feature's part in the accounts' own keys (ADR 71).
type Sealer struct {
	// Reseal moves what the feature keeps for the account under the key the ring gives it now, and does
	// nothing where that is done. It runs at every sign-in, so a move cut short goes on.
	Reseal func(account string) error
	// Drop deletes what the feature keeps for the account under an own key that is lost.
	Drop func(account string) error
}

var (
	// ErrLocked is a write to an account whose encryption is on while its key is not open.
	ErrLocked = keyring.ErrLocked
	// ErrRecoveryKey is a recovery key that does not open the account's key.
	ErrRecoveryKey = errors.New("alborz: the recovery key does not open the account's key")
)

// unlock opens the account's own key with the login's password and holds it for the login's life. A key
// the password does not open, since it changed elsewhere, is taken from another login that holds it and
// wrapped under the new password; without one the login is locked.
func (s *Server) unlock(l *Login) error {
	ctx, cancel := context.WithTimeout(context.Background(), RoundTripTimeout)
	defer cancel()
	name := s.Keys.Name(l.username)
	k, err := keyOf(ctx, l.store)
	if err != nil {
		return err
	}
	own := k.open(l.password)
	held, ok := s.Keys.Own(name)
	if own == nil && ok && keyID(held) == k.ID {
		own = held
		if err := l.store.Put(ctx, keyEntry, k.under(own, l.password)); err != nil {
			return err
		}
	}
	old := k.retiredBy(l.password)
	if ok && keyID(held) != k.ID {
		old = append(old, held)
	}
	s.Keys.Retire(name, old...)
	if l.release, err = s.Keys.Hold(name, own, k.on()); err != nil {
		return err
	}
	return s.settle(l)
}

// settle marks the login locked while no key at hand opens the account's pair, and otherwise moves what the
// account keeps under its key now; once all is moved, the keys it replaced are let go.
func (s *Server) settle(l *Login) error {
	name := s.Keys.Name(l.username)
	l.locked.Store(s.Keys.Locked(name))
	if l.Locked() {
		return nil
	}
	if err := s.reseal(l); err != nil {
		s.log.Printf("%s: moving what alborz keeps under the account's key: %v", l.username, err)
		return nil
	}
	return s.Keys.Settled(name)
}

func (s *Server) reseal(l *Login) error {
	var errs []error
	if st, ok := l.store.(localStore); ok {
		errs = append(errs, st.records.reseal(l.username))
	}
	ctx, cancel := context.WithTimeout(context.Background(), RoundTripTimeout)
	defer cancel()
	errs = append(errs, l.resealHTTPPassword(ctx))
	for _, sealer := range s.Sealers {
		errs = append(errs, sealer.Reseal(l.username))
	}
	return errors.Join(errs...)
}

// Encryption is an account's state for its page.
type Encryption struct {
	On, Locked bool
	// OnServer says the key is kept on the mail server, not in alborz's file.
	OnServer bool
}

func (s *Server) Encryption(ctx context.Context, l *Login) (Encryption, error) {
	k, err := keyOf(ctx, l.store)
	_, metadata := l.store.(*imapMetadataStore)
	return Encryption{On: k.on(), Locked: l.Locked(), OnServer: metadata}, err
}

// confirm checks password against the login's, counting a wrong one as a refused sign-in (ADR 30).
func (s *Server) confirm(l *Login, password, from string) error {
	return s.signInRefused.attempt(readerKey(from, l.username), func() error {
		if subtle.ConstantTimeCompare([]byte(password), []byte(l.password)) != 1 {
			return AuthError{errors.New("not the account's password")}
		}
		return nil
	})
}

// Encrypt turns the account's encryption on, making its key the first time, and returns a new recovery key,
// which nothing keeps: the reader writes it down.
func (s *Server) Encrypt(ctx context.Context, l *Login, password, from string) (string, error) {
	if err := s.confirm(l, password, from); err != nil {
		return "", err
	}
	if l.Locked() {
		return "", ErrLocked
	}
	k, err := keyOf(ctx, l.store)
	if err != nil {
		return "", err
	}
	k, own, recovery := k.renewed(l.password)
	if err := l.store.Put(ctx, keyEntry, &k); err != nil {
		return "", err
	}
	if err := s.Keys.Turn(s.Keys.Name(l.username), own, true); err != nil {
		return "", err
	}
	return recovery, s.moved(l)
}

// moved is settle after the reader's own action, which reports what did not move.
func (s *Server) moved(l *Login) error {
	if err := s.reseal(l); err != nil {
		return err
	}
	return s.Keys.Settled(s.Keys.Name(l.username))
}

// Decrypt turns the account's encryption off: what it keeps goes back under the instance's keys.
func (s *Server) Decrypt(ctx context.Context, l *Login, password, from string) error {
	if err := s.confirm(l, password, from); err != nil {
		return err
	}
	if l.Locked() {
		return ErrLocked
	}
	k, err := keyOf(ctx, l.store)
	if err != nil || !k.on() {
		return err
	}
	k.Off = true
	if err := l.store.Put(ctx, keyEntry, &k); err != nil {
		return err
	}
	if err := s.Keys.Turn(s.Keys.Name(l.username), nil, false); err != nil {
		return err
	}
	return s.moved(l)
}

// Recover opens a locked account with a recovery key: the current key's, which is then wrapped under the
// login's password, or, while the current key is open, a retired key's, whose values here are sealed again.
func (s *Server) Recover(ctx context.Context, l *Login, recovery string) error {
	k, err := keyOf(ctx, l.store)
	if err != nil {
		return err
	}
	name, key := s.Keys.Name(l.username), recoveryKey(recovery)
	if own, err := key.Open(k.ByRecovery); err == nil && len(own) == seal.KeySize {
		k = k.under(own, l.password)
		if err := l.store.Put(ctx, keyEntry, &k); err != nil {
			return err
		}
		if err := s.Keys.Turn(name, own, k.on()); err != nil {
			return err
		}
		l.forgetHTTPPassword()
		return s.recovered(l)
	}
	if held, ok := s.Keys.Own(name); !ok || keyID(held) != k.ID {
		return ErrRecoveryKey
	}
	for _, old := range k.Retired {
		if own, err := key.Open(old.ByRecovery); err == nil && len(own) == seal.KeySize {
			s.Keys.Retire(name, own)
			if err := s.Keys.Turn(name, nil, k.on()); err != nil {
				return err
			}
			l.forgetHTTPPassword()
			return s.recovered(l)
		}
	}
	return ErrRecoveryKey
}

func (s *Server) recovered(l *Login) error {
	l.locked.Store(s.Keys.Locked(s.Keys.Name(l.username)))
	if l.Locked() {
		return ErrRecoveryKey
	}
	return s.moved(l)
}

// StartOver deletes what a locked account keeps here under a lost key: the reader's choice when neither the
// password nor a recovery key opens it. Where the current key is lost too, a new one replaces it, with its
// recovery key returned, and the lost one stays retired for another alborz that still holds it.
func (s *Server) StartOver(ctx context.Context, l *Login) (string, error) {
	if !l.Locked() {
		return "", errors.New("alborz: the account's key is open; there is nothing to start over from")
	}
	var errs []error
	if st, ok := l.store.(localStore); ok {
		errs = append(errs, st.records.drop(l.username))
	}
	errs = append(errs, l.dropHTTPPassword(ctx))
	for _, sealer := range s.Sealers {
		errs = append(errs, sealer.Drop(l.username))
	}
	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	k, err := keyOf(ctx, l.store)
	if err != nil {
		return "", err
	}
	name, recovery := s.Keys.Name(l.username), ""
	own, ok := s.Keys.Own(name)
	if !ok || keyID(own) != k.ID {
		k, own, recovery = k.renewed(l.password)
		if err := l.store.Put(ctx, keyEntry, &k); err != nil {
			return "", err
		}
	}
	if err := s.Keys.Replace(name, own); err != nil {
		return "", err
	}
	l.forgetHTTPPassword()
	l.locked.Store(false)
	return recovery, s.moved(l)
}

// Unlock opens the account's key for a client that sends the password with every request and holds no
// login, such as a phone syncing, and keeps it for d past each request, so a phone that keeps syncing
// opens it once. A password that does not open it is not tried again for d, as each try is a sign-in.
func (s *Server) Unlock(username, password string, d time.Duration) error {
	name := s.Keys.Name(username)
	if !s.Keys.Locked(name) {
		s.Keys.Lease(name, d)
		return nil
	}
	if at, ok := s.unlockRefused.Load(name); ok && time.Since(at.(time.Time)) < d {
		return nil
	}
	l, err := s.logins.put(username, password, "")
	if err != nil {
		return err
	}
	defer l.Close()
	if l.Locked() {
		s.unlockRefused.Store(name, time.Now())
		return nil
	}
	s.unlockRefused.Delete(name)
	s.Keys.Lease(name, d)
	return nil
}
