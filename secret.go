package alborz

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"git.mehdix.org/alborz/internal/seal"
	"golang.org/x/crypto/argon2"
)

// An envelope keeps a secret under a random key, wrapped under a key derived
// from the mail password.
type envelope struct {
	Secret     string
	Salt       string
	ByPassword string
}

// ErrSealed says the envelope no longer opens and the secret must be entered again.
var ErrSealed = errors.New("alborz: the stored secret cannot be read any more")

const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	envelopeSalt = 16
)

func passwordKey(password string, salt []byte) *seal.Key {
	return seal.NewKey([seal.KeySize]byte(argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, seal.KeySize)))
}

func wrap(secret []byte, k *seal.Key) string {
	return base64.URLEncoding.EncodeToString(k.Seal(secret))
}

// unwrap opens tok, nil where k does not; a Fernet token of earlier builds decodes to another format.
func unwrap(tok string, k *seal.Key) []byte {
	sealed, err := base64.URLEncoding.DecodeString(tok)
	if err != nil {
		return nil
	}
	plain, _ := k.Open(sealed)
	return plain
}

// seal wraps plain for this account under its mail password.
func (l *Login) seal(plain string) envelope {
	var k [seal.KeySize]byte
	rand.Read(k[:])
	salt := make([]byte, envelopeSalt)
	rand.Read(salt)
	return envelope{
		Secret:     wrap([]byte(plain), seal.NewKey(k)),
		Salt:       base64.StdEncoding.EncodeToString(salt),
		ByPassword: wrap(k[:], passwordKey(l.password, salt)),
	}
}

// open reads the secret with the mail password.
func (l *Login) open(e envelope) (string, error) {
	salt, err := base64.StdEncoding.DecodeString(e.Salt)
	if err != nil {
		return "", err
	}
	raw := unwrap(e.ByPassword, passwordKey(l.password, salt))
	if len(raw) != seal.KeySize {
		return "", ErrSealed
	}
	plain := unwrap(e.Secret, seal.NewKey([seal.KeySize]byte(raw)))
	if plain == nil {
		return "", ErrSealed
	}
	return string(plain), nil
}

// httpPasswordKey stores the HTTP services' password when it is not the mail password.
const httpPasswordKey = "http-password"

// HTTPPassword returns the stored HTTP password, else the login password. One the
// mail password no longer opens stays stored, for the reader to enter again or clear.
func (l *Login) HTTPPassword(ctx context.Context) (string, error) {
	l.httpLocker.Lock()
	defer l.httpLocker.Unlock()
	if l.httpSealed {
		return "", ErrSealed
	}
	if l.httpLoaded {
		return l.httpPassword, nil
	}
	var e envelope
	err := l.store.Get(ctx, httpPasswordKey, &e)
	if err == ErrNoStoreEntry || (err == nil && e.Secret == "") {
		l.httpPassword, l.httpLoaded = l.password, true
		return l.password, nil
	}
	if err != nil {
		return "", err
	}
	plain, err := l.open(e)
	l.httpSealed = errors.Is(err, ErrSealed)
	if err != nil {
		return "", err
	}
	l.httpPassword, l.httpLoaded = plain, true
	return plain, nil
}

// HasHTTPPassword reports whether one is kept, without opening it.
func (l *Login) HasHTTPPassword(ctx context.Context) (bool, error) {
	var e envelope
	err := l.store.Get(ctx, httpPasswordKey, &e)
	if err == ErrNoStoreEntry {
		return false, nil
	}
	return err == nil && e.Secret != "", err
}

// SetHTTPPassword keeps password for HTTP services; empty or the mail password
// reverts to the mail password.
func (l *Login) SetHTTPPassword(ctx context.Context, password string) error {
	if password == l.password {
		password = ""
	}
	var e envelope
	if password != "" {
		e = l.seal(password)
	}
	if err := l.store.Put(ctx, httpPasswordKey, &e); err != nil {
		return err
	}
	if password == "" {
		password = l.password
	}
	l.httpLocker.Lock()
	l.httpPassword, l.httpLoaded, l.httpSealed = password, true, false
	l.httpLocker.Unlock()
	return nil
}

// rewrap seals the kept HTTP password under next, the mail password replacing the login's.
// One the login's password no longer opens stays as it is (ADR 5).
func (l *Login) rewrap(ctx context.Context, next string) error {
	var e envelope
	err := l.store.Get(ctx, httpPasswordKey, &e)
	if err == ErrNoStoreEntry || (err == nil && e.Secret == "") {
		return nil
	}
	if err != nil {
		return err
	}
	salt, err := base64.StdEncoding.DecodeString(e.Salt)
	if err != nil {
		return err
	}
	k := unwrap(e.ByPassword, passwordKey(l.password, salt))
	if k == nil {
		return nil
	}
	salt = make([]byte, envelopeSalt)
	rand.Read(salt)
	e.Salt, e.ByPassword = base64.StdEncoding.EncodeToString(salt), wrap(k, passwordKey(next, salt))
	return l.store.Put(ctx, httpPasswordKey, &e)
}
