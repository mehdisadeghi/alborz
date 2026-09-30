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

// gcm is the format byte of AES-256-GCM with a random 96-bit nonce, the only one written so far. It
// leads every sealed value, so a later format is told apart from this one by its first byte.
const gcm byte = 1

var (
	// ErrFormat is a value in no format this build reads, such as the Fernet tokens earlier builds wrote.
	ErrFormat = errors.New("seal: not in a format this alborz reads")
	// ErrOpen is a value the key does not open: another key sealed it, or it was changed.
	ErrOpen = errors.New("seal: the key does not open the value")
)

// Key seals values and opens them.
type Key struct{ aead cipher.AEAD }

func NewKey(secret [KeySize]byte) *Key {
	block, err := aes.NewCipher(secret[:])
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		panic(err)
	}
	return &Key{aead}
}

// Seal returns plain sealed, behind its format byte, which the seal covers too.
func (k *Key) Seal(plain []byte) []byte {
	return k.aead.Seal([]byte{gcm}, nil, plain, []byte{gcm})
}

func (k *Key) Open(sealed []byte) ([]byte, error) {
	if len(sealed) == 0 || sealed[0] != gcm {
		return nil, ErrFormat
	}
	plain, err := k.aead.Open(nil, nil, sealed[1:], sealed[:1])
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}

// Root is the operator's login key. Nothing is sealed under it directly: each purpose derives a key
// of its own, so no two purposes ever share one.
type Root struct{ secret [KeySize]byte }

// ParseRoot reads a login key: 32 bytes in base64, either alphabet, so a Fernet key serves as well.
func ParseRoot(text string) (Root, error) {
	var r Root
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
func (r Root) Key(purpose string) *Key { return NewKey(r.derive("alborz seal " + purpose)) }

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
