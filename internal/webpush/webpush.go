// Package webpush sends Web Push messages (RFC 8030), encrypted to the browser (RFC 8291) and signed by the
// application server's VAPID key (RFC 8292), with the standard library alone.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Subscription is what a browser's PushManager hands the page: where to post, and the keys to encrypt to.
type Subscription struct {
	Endpoint string
	// P256dh is the browser's public key, an uncompressed P-256 point; Auth its 16-byte secret.
	P256dh, Auth []byte
}

// Parse reads a subscription as a page's PushSubscription.toJSON gives it: an https endpoint, and the
// keys in unpadded base64url.
func Parse(endpoint, p256dh, auth string) (Subscription, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return Subscription{}, fmt.Errorf("webpush: the endpoint %q is not an https URL", endpoint)
	}
	sub := Subscription{Endpoint: endpoint}
	if sub.P256dh, err = b64.DecodeString(strings.TrimRight(p256dh, "=")); err != nil {
		return Subscription{}, fmt.Errorf("webpush: p256dh: %w", err)
	}
	if _, err := ecdh.P256().NewPublicKey(sub.P256dh); err != nil {
		return Subscription{}, fmt.Errorf("webpush: p256dh: %w", err)
	}
	if sub.Auth, err = b64.DecodeString(strings.TrimRight(auth, "=")); err != nil || len(sub.Auth) != authSize {
		return Subscription{}, errors.New("webpush: auth is not 16 octets in base64url")
	}
	return sub, nil
}

// recordSize is RFC 8188's rs; one record holds every message, since RFC 8030 bounds a payload at 4096.
const (
	recordSize = 4096
	saltSize   = 16
	headerSize = saltSize + 4 + 1 + 65
	tagSize    = 16
	authSize   = 16
	// MaxPayload is what fits one record beside its delimiter and tag.
	MaxPayload = recordSize - headerSize - tagSize - 1
)

var b64 = base64.RawURLEncoding

// Encrypt seals plaintext to the subscription as an aes128gcm message (RFC 8291 section 3).
func Encrypt(sub Subscription, plaintext []byte) ([]byte, error) {
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltSize)
	rand.Read(salt)
	return encrypt(sub, plaintext, as, salt)
}

func encrypt(sub Subscription, plaintext []byte, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > MaxPayload {
		return nil, fmt.Errorf("webpush: a payload of %d bytes is over %d", len(plaintext), MaxPayload)
	}
	ua, err := ecdh.P256().NewPublicKey(sub.P256dh)
	if err != nil {
		return nil, fmt.Errorf("webpush: the browser's key: %w", err)
	}
	asPublic := as.PublicKey().Bytes()
	gcm, nonce, err := contentKey(as, ua, sub.P256dh, asPublic, sub.Auth, salt)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, headerSize+len(plaintext)+1+tagSize)
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	// 0x02 closes the last record (RFC 8188 section 2).
	return gcm.Seal(out, nonce, append(plaintext, 2), nil), nil
}

// Decrypt opens a message sealed to the browser holding ua and auth; the counterpart a test push service uses.
func Decrypt(ua *ecdh.PrivateKey, auth, message []byte) ([]byte, error) {
	if len(message) < headerSize+tagSize || message[saltSize+4] != 65 {
		return nil, errors.New("webpush: not a single-record aes128gcm message")
	}
	salt, asPublic := message[:saltSize], message[saltSize+5:headerSize]
	as, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, err
	}
	gcm, nonce, err := contentKey(ua, as, ua.PublicKey().Bytes(), asPublic, auth, salt)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, message[headerSize:], nil)
	if err != nil {
		return nil, err
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		return nil, errors.New("webpush: no last-record delimiter")
	}
	return plain[:len(plain)-1], nil
}

// contentKey derives the AES-128-GCM key and nonce from one side's private key and the other's public
// key (RFC 8291 section 3.4).
func contentKey(own *ecdh.PrivateKey, other *ecdh.PublicKey, uaPublic, asPublic, auth, salt []byte) (cipher.AEAD, []byte, error) {
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

// Key is the application server's VAPID key pair (RFC 8292); a browser accepts pushes signed by the key
// it subscribed with, and no other.
type Key struct{ private *ecdsa.PrivateKey }

// NewKey makes a key pair.
func NewKey() (*Key, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return &Key{k}, err
}

// ParseKey reads a private key as Bytes wrote it.
func ParseKey(raw []byte) (*Key, error) {
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	return &Key{k}, err
}

// Bytes is the private key, for keeping.
func (k *Key) Bytes() []byte {
	raw, err := k.private.Bytes()
	if err != nil {
		panic(err)
	}
	return raw
}

// Public is the public key as a page passes it to PushManager.subscribe (applicationServerKey).
func (k *Key) Public() string {
	raw, err := k.private.PublicKey.Bytes()
	if err != nil {
		panic(err)
	}
	return b64.EncodeToString(raw)
}

// authorization is the vapid header for endpoint: an ES256 JWT naming the push service's origin, valid
// until exp, and the contact subject (RFC 8292 section 2).
func (k *Key) authorization(endpoint, subject string, exp time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": exp.Unix(), "sub": subject})
	if err != nil {
		return "", err
	}
	signed := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, k.private, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signed + "." + b64.EncodeToString(sig) + ", k=" + k.Public(), nil
}

// Message is one push: TTL is how long the push service may hold it for a browser that is away; Topic,
// when set, replaces an undelivered message of the same topic; Urgency, when set, is very-low, low,
// normal or high (RFC 8030 sections 5.2 to 5.4).
type Message struct {
	Payload []byte
	TTL     time.Duration
	Topic   string
	Urgency string
}

// jwtLife is well inside RFC 8292's 24 hours, so a clock a little off does not void it.
const jwtLife = 12 * time.Hour

// Send encrypts and posts m to sub, signed by key for subject (a mailto: or https: contact). It returns the
// push service's status: 201 delivered to the service, 404 or 410 a subscription that is gone.
func Send(ctx context.Context, client *http.Client, key *Key, subject string, sub Subscription, m Message) (int, error) {
	body, err := Encrypt(sub, m.Payload)
	if err != nil {
		return 0, err
	}
	auth, err := key.authorization(sub.Endpoint, subject, time.Now().Add(jwtLife))
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(m.TTL.Seconds())))
	if m.Topic != "" {
		req.Header.Set("Topic", m.Topic)
	}
	if m.Urgency != "" {
		req.Header.Set("Urgency", m.Urgency)
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	res.Body.Close()
	return res.StatusCode, nil
}

// Verify checks a vapid Authorization header by the public key it names and returns that key and the
// JWT's claims; the check a push service makes, for a test one.
func Verify(header string) (public string, claims map[string]any, err error) {
	token, public, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	signed, sig64, cut := strings.Cut(token, ".")
	if !ok || !strings.HasPrefix(header, "vapid ") || !cut {
		return "", nil, errors.New("webpush: not a vapid header")
	}
	payload, sig64, _ := strings.Cut(sig64, ".")
	signed += "." + payload
	raw, err := b64.DecodeString(public)
	if err != nil {
		return "", nil, err
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return "", nil, err
	}
	sig, err := b64.DecodeString(sig64)
	if err != nil || len(sig) != 64 {
		return "", nil, errors.New("webpush: not an ES256 signature")
	}
	digest := sha256.Sum256([]byte(signed))
	if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return "", nil, errors.New("webpush: the signature does not verify")
	}
	body, err := b64.DecodeString(payload)
	if err != nil {
		return "", nil, err
	}
	return public, claims, json.Unmarshal(body, &claims)
}
