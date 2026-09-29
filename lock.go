package alborz

import (
	"encoding/base64"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// defaultLockAfter is how long a session with a passkey may idle before it locks.
const defaultLockAfter = LoginDuration

// MinLockAfter keeps a typed lock time from locking between two clicks.
const MinLockAfter = time.Minute

// Passkey is a WebAuthn credential a browser registered for its session.
type Passkey struct {
	Name       string
	Added      time.Time
	LastUsed   time.Time
	Credential webauthn.Credential
}

// ID names the passkey in a form by its credential id.
func (p Passkey) ID() string {
	return base64.RawURLEncoding.EncodeToString(p.Credential.ID)
}

// Lock holds a session's passkeys and how long it may idle before they are asked for.
type Lock struct {
	Passkeys []Passkey
	After    time.Duration
}

// Wait is the lock time in force.
func (l Lock) Wait() time.Duration {
	if l.After < MinLockAfter {
		return defaultLockAfter
	}
	return l.After
}
