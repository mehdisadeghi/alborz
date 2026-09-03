package alborz

import (
	"crypto/rand"
	"encoding/base64"
	"errors"

	"github.com/fernet/fernet-go"
	"golang.org/x/crypto/argon2"
)

// An Envelope keeps a secret readable after either key that guards it
// changes. The secret is under a random key, and that key is wrapped
// twice: under the server's login key and under a key derived from the
// mail password. Whichever still opens re-wraps the other, so a key
// rotation or a password change costs nothing; only both changing
// before the next sign-in loses the secret.
type Envelope struct {
	Secret     string
	Salt       string
	ByServer   string
	ByPassword string
}

// ErrSealed says neither wrap opened: the secret has to be entered
// again.
var ErrSealed = errors.New("alborz: the stored secret cannot be read any more")

// Argon2id parameters: the derivation runs once per session, so the
// cost can sit at the RFC 9106 second recommendation (64 MiB, one
// pass) rather than the interactive minimum.
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
		panic(err) // AES-CBC over a key-sized buffer cannot fail
	}
	return string(tok)
}

func unwrap(tok string, k *fernet.Key) []byte {
	if tok == "" || k == nil {
		return nil
	}
	return fernet.VerifyAndDecrypt([]byte(tok), 0, []*fernet.Key{k})
}

// Seal wraps plain for this account under both of its keys.
func (s *Session) Seal(plain string) (Envelope, error) {
	var k fernet.Key
	if err := k.Generate(); err != nil {
		return Envelope{}, err
	}
	salt := make([]byte, envelopeSalt)
	if _, err := rand.Read(salt); err != nil {
		return Envelope{}, err
	}
	return Envelope{
		Secret:     wrap([]byte(plain), &k),
		Salt:       base64.StdEncoding.EncodeToString(salt),
		ByServer:   wrapServer(k[:], s.manager.loginKey),
		ByPassword: wrap(k[:], passwordKey(s.password, salt)),
	}, nil
}

func wrapServer(secret []byte, k *fernet.Key) string {
	if k == nil {
		return ""
	}
	return wrap(secret, k)
}

// Open reads the secret and re-wraps the key that no longer opened.
// The envelope returned is the one to keep; it differs from e only when
// a wrap was renewed.
func (s *Session) Open(e Envelope) (string, Envelope, error) {
	salt, err := base64.StdEncoding.DecodeString(e.Salt)
	if err != nil {
		return "", e, err
	}
	pk := passwordKey(s.password, salt)
	byServer := unwrap(e.ByServer, s.manager.loginKey)
	byPassword := unwrap(e.ByPassword, pk)
	raw := byServer
	if raw == nil {
		raw = byPassword
	}
	if raw == nil {
		return "", e, ErrSealed
	}
	var k fernet.Key
	copy(k[:], raw)
	plain := unwrap(e.Secret, &k)
	if plain == nil {
		return "", e, ErrSealed
	}
	if byServer == nil {
		e.ByServer = wrapServer(raw, s.manager.loginKey)
	}
	if byPassword == nil {
		e.ByPassword = wrap(raw, pk)
	}
	return string(plain), e, nil
}

// httpPasswordKey is where the store keeps the password the account's
// HTTP services take, when it is not the mail password.
const httpPasswordKey = "http-password"

// HTTPPassword is what the account's HTTP services are given: the one
// kept for them when there is one, the login password otherwise. The
// envelope is opened once per session; the derivation is not cheap.
func (s *Session) HTTPPassword() (string, error) {
	s.httpLocker.Lock()
	defer s.httpLocker.Unlock()
	if s.httpLoaded {
		return s.httpPassword, nil
	}
	var e Envelope
	err := s.store.Get(httpPasswordKey, &e)
	if err == ErrNoStoreEntry || (err == nil && e.Secret == "") {
		s.httpPassword, s.httpLoaded = s.password, true
		return s.password, nil
	}
	if err != nil {
		return "", err
	}
	plain, fresh, err := s.Open(e)
	if err != nil {
		return "", err
	}
	if fresh != e {
		if err := s.store.Put(httpPasswordKey, &fresh); err != nil {
			return "", err
		}
	}
	s.httpPassword, s.httpLoaded = plain, true
	return plain, nil
}

// HasHTTPPassword reports whether one is kept, without opening it.
func (s *Session) HasHTTPPassword() (bool, error) {
	var e Envelope
	err := s.store.Get(httpPasswordKey, &e)
	if err == ErrNoStoreEntry {
		return false, nil
	}
	return err == nil && e.Secret != "", err
}

// SetHTTPPassword keeps password for the account's HTTP services; empty
// goes back to the login password.
func (s *Session) SetHTTPPassword(password string) error {
	var e Envelope
	if password != "" {
		var err error
		if e, err = s.Seal(password); err != nil {
			return err
		}
	}
	if err := s.store.Put(httpPasswordKey, &e); err != nil {
		return err
	}
	s.httpLocker.Lock()
	s.httpPassword, s.httpLoaded = password, password != ""
	if password == "" {
		s.httpPassword, s.httpLoaded = s.password, true
	}
	s.httpLocker.Unlock()
	return nil
}
