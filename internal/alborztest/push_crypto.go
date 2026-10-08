package alborztest

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
)

const (
	pushSaltSize   = 16
	pushHeaderSize = pushSaltSize + 4 + 1 + 65
	pushTagSize    = 16
)

var pushB64 = base64.RawURLEncoding

func decryptPush(ua *ecdh.PrivateKey, auth, message []byte) ([]byte, error) {
	if len(message) < pushHeaderSize+pushTagSize || message[pushSaltSize+4] != 65 {
		return nil, errors.New("webpush: not a single-record aes128gcm message")
	}
	salt, asPublic := message[:pushSaltSize], message[pushSaltSize+5:pushHeaderSize]
	as, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, err
	}
	gcm, nonce, err := pushContentKey(ua, as, ua.PublicKey().Bytes(), asPublic, auth, salt)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, message[pushHeaderSize:], nil)
	if err != nil {
		return nil, err
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		return nil, errors.New("webpush: no last-record delimiter")
	}
	return plain[:len(plain)-1], nil
}

func pushContentKey(own *ecdh.PrivateKey, other *ecdh.PublicKey, uaPublic, asPublic, auth, salt []byte) (cipher.AEAD, []byte, error) {
	shared, err := own.ECDH(other)
	if err != nil {
		return nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		return nil, nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prk, "WebPush: info\x00"+string(uaPublic)+string(asPublic), 32)
	if err != nil {
		return nil, nil, err
	}
	prk, err = hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	return gcm, nonce, err
}

func verifyPush(header string) (public string, claims map[string]any, err error) {
	token, public, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	signed, sig64, cut := strings.Cut(token, ".")
	if !ok || !strings.HasPrefix(header, "vapid ") || !cut {
		return "", nil, errors.New("webpush: not a vapid header")
	}
	payload, sig64, _ := strings.Cut(sig64, ".")
	signed += "." + payload
	raw, err := pushB64.DecodeString(public)
	if err != nil {
		return "", nil, err
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return "", nil, err
	}
	sig, err := pushB64.DecodeString(sig64)
	if err != nil || len(sig) != 64 {
		return "", nil, errors.New("webpush: not an ES256 signature")
	}
	digest := sha256.Sum256([]byte(signed))
	if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return "", nil, errors.New("webpush: the signature does not verify")
	}
	body, err := pushB64.DecodeString(payload)
	if err != nil {
		return "", nil, err
	}
	return public, claims, json.Unmarshal(body, &claims)
}
