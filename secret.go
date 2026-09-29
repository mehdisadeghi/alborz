package alborz

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"github.com/fernet/fernet-go"
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

func passwordKey(password string, salt []byte) *fernet.Key {
	var k fernet.Key
	copy(k[:], argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, uint32(len(k))))
	return &k
}

func wrap(secret []byte, k *fernet.Key) string {
	tok, err := fernet.EncryptAndSign(secret, k)
	if err != nil {
		panic(err)
	}
	return string(tok)
}

func unwrap(tok string, k *fernet.Key) []byte {
	if tok == "" {
		return nil
	}
	return fernet.VerifyAndDecrypt([]byte(tok), 0, []*fernet.Key{k})
}

// seal wraps plain for this account under its mail password.
func (l *Login) seal(plain string) (envelope, error) {
	var k fernet.Key
	if err := k.Generate(); err != nil {
		return envelope{}, err
	}
	salt := make([]byte, envelopeSalt)
	if _, err := rand.Read(salt); err != nil {
		return envelope{}, err
	}
	return envelope{
		Secret:     wrap([]byte(plain), &k),
		Salt:       base64.StdEncoding.EncodeToString(salt),
		ByPassword: wrap(k[:], passwordKey(l.password, salt)),
	}, nil
}

// open reads the secret with the mail password.
func (l *Login) open(e envelope) (string, error) {
	salt, err := base64.StdEncoding.DecodeString(e.Salt)
	if err != nil {
		return "", err
	}
	raw := unwrap(e.ByPassword, passwordKey(l.password, salt))
	if raw == nil {
		return "", ErrSealed
	}
	var k fernet.Key
	copy(k[:], raw)
	plain := unwrap(e.Secret, &k)
	if plain == nil {
		return "", ErrSealed
	}
	return string(plain), nil
}

// httpPasswordKey stores the HTTP services' password when it is not the mail password.
const httpPasswordKey = "http-password"

// HTTPPassword returns the kept HTTP password, else the login password. One the
// mail password no longer opens stays kept, for the reader to enter again or clear.
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
		var err error
		if e, err = l.seal(password); err != nil {
			return err
		}
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
