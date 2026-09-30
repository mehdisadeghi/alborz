// Package seal keeps what alborz writes unreadable and unforgeable without its key, and names what it
// keeps by keyed hashes (ADR 70).
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// KeySize is the length of every key: AES-256.
const KeySize = 32

// A format byte leads every sealed value and says which kind of key sealed it, so a reader knows which to
// reach for. Both are AES-256-GCM with a random 96-bit nonce.
const (
	// ByInstance is a key the instance holds: one derived from the login key, or one its context names.
	ByInstance byte = 1
	// ByAccount is a key an account holds: its own, or one of its collections' (ADR 71).
	ByAccount byte = 2
)

var (
	// ErrFormat is a value in no format this build reads, such as the Fernet tokens earlier builds wrote.
	ErrFormat = errors.New("seal: not in a format this alborz reads")
	// ErrOpen is a value the key does not open: another key sealed it, or it was changed.
	ErrOpen = errors.New("seal: the key does not open the value")
)

// Key seals values and opens them.
type Key struct {
	aead   cipher.AEAD
	format byte
}

// NewKey is a key the instance holds.
func NewKey(secret [KeySize]byte) *Key { return newKey(secret, ByInstance) }

// NewAccountKey is a key an account holds.
func NewAccountKey(secret [KeySize]byte) *Key { return newKey(secret, ByAccount) }

func newKey(secret [KeySize]byte, format byte) *Key {
	block, err := aes.NewCipher(secret[:])
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		panic(err)
	}
	return &Key{aead, format}
}

// Format is the byte k's values begin with.
func (k *Key) Format() byte { return k.format }

// Format is the byte sealed begins with, 0 for an empty value.
func Format(sealed []byte) byte {
	if len(sealed) == 0 {
		return 0
	}
	return sealed[0]
}

// Seal returns plain sealed, behind its format byte, which the seal covers too.
func (k *Key) Seal(plain []byte) []byte {
	return k.aead.Seal([]byte{k.format}, nil, plain, []byte{k.format})
}

func (k *Key) Open(sealed []byte) ([]byte, error) {
	if f := Format(sealed); f != ByInstance && f != ByAccount {
		return nil, ErrFormat
	}
	plain, err := k.aead.Open(nil, nil, sealed[1:], sealed[:1])
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}

// Root is the operator's login key, or an account's own. Nothing is sealed under it directly: each
// purpose derives a key of its own, so no two purposes ever share one.
type Root struct {
	secret [KeySize]byte
	format byte
}

// AccountRoot is an account's own key.
func AccountRoot(secret [KeySize]byte) Root { return Root{secret, ByAccount} }

// ParseRoot reads a login key: 32 bytes in base64, either alphabet, so a Fernet key serves as well.
func ParseRoot(text string) (Root, error) {
	r := Root{format: ByInstance}
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(text)
	}
	if err != nil || len(raw) != KeySize {
		return r, fmt.Errorf("want %d random bytes in base64, as `openssl rand -base64 %d` writes", KeySize, KeySize)
	}
	copy(r.secret[:], raw)
	return r, nil
}

func (r Root) derive(info string) [KeySize]byte {
	raw, err := hkdf.Key(sha256.New, r.secret[:], nil, info, KeySize)
	if err != nil {
		panic(err)
	}
	return [KeySize]byte(raw)
}

// Key is the key sealing what purpose keeps.
func (r Root) Key(purpose string) *Key { return newKey(r.derive("alborz seal "+purpose), r.format) }

// Names names what alborz keeps by keyed hashes.
func (r Root) Names() Names {
	k := r.derive("alborz names")
	return Names{key: k[:]}
}

// Names derives the name of a record or file from what it is about.
type Names struct{ key []byte }

// Of is s's name: a keyed hash, so nobody without the login key can confirm s by hashing it.
func (n Names) Of(s string) string {
	mac := hmac.New(sha256.New, n.key)
	mac.Write([]byte(s))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}
