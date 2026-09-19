package alborz

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// DefaultLockAfter is how long a reader may do nothing before a visit
// with a passkey asks for it again: long enough to read a long message
// and answer the door, short enough that a phone left behind is locked
// before it is found. It is a session's life, so the two ask together.
const DefaultLockAfter = sessionDuration

// MinLockAfter keeps a typed lock time from locking between two clicks.
const MinLockAfter = time.Minute

// Passkey is a WebAuthn credential a browser registered for its visit.
// WebAuthn lets a user hold several - the phone's own, a hardware key
// kept for when the phone is gone - so a visit keeps a list.
type Passkey struct {
	Name       string
	Added      time.Time
	LastUsed   time.Time
	Credential webauthn.Credential
}

// ID names the passkey in a form: the credential's own id, which the
// authenticator chose and which stays put while others come and go.
func (p Passkey) ID() string {
	return base64.RawURLEncoding.EncodeToString(p.Credential.ID)
}

// RemovePasskey takes one passkey off the visit; false when it has no
// passkey of that id. Removing the last one removes the lock.
func (v *Visit) RemovePasskey(id string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i, p := range v.lock.Passkeys {
		if p.ID() == id {
			v.lock.Passkeys = append(v.lock.Passkeys[:i:i], v.lock.Passkeys[i+1:]...)
			return true
		}
	}
	return false
}

// SetLockAfter changes how long the visit waits before it locks.
func (v *Visit) SetLockAfter(d time.Duration) {
	v.mu.Lock()
	v.lock.After = d
	v.mu.Unlock()
}

// Lock is what stands in front of a visit: the passkeys that open it,
// and how long the reader may be away before they are asked for. A
// visit with no passkey has no lock.
type Lock struct {
	Passkeys []Passkey
	After    time.Duration
}

// Wait is the lock time in force.
func (l Lock) Wait() time.Duration {
	if l.After < MinLockAfter {
		return DefaultLockAfter
	}
	return l.After
}

// Lock is the visit's lock as it stands.
func (v *Visit) Lock() Lock {
	v.mu.Lock()
	defer v.mu.Unlock()
	return Lock{Passkeys: append([]Passkey(nil), v.lock.Passkeys...), After: v.lock.After}
}

// locks reports whether the visit has a passkey to lock behind.
func (v *Visit) locks() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.lock.Passkeys) > 0
}

// Locked reports whether the visit wants its passkey, deciding it when
// the reader has been away too long. A visit rebuilt after a restart
// has no activity on record and starts locked.
func (v *Visit) Locked(now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.lock.Passkeys) == 0 {
		return false
	}
	if now.Sub(v.active) > v.lock.Wait() {
		v.locked = true
	}
	return v.locked
}

// act notes that the reader did something.
func (v *Visit) act(now time.Time) {
	v.mu.Lock()
	v.active = now
	v.mu.Unlock()
}

// LockNow locks the visit as if the lock time had run out.
func (v *Visit) LockNow() {
	v.mu.Lock()
	v.locked = true
	v.mu.Unlock()
}

func (v *Visit) unlock(now time.Time) {
	v.mu.Lock()
	v.locked, v.active = false, now
	v.mu.Unlock()
}

// addPasskey keeps a new passkey; registering the first is the reader
// being here, so it does not lock them out at once.
func (v *Visit) addPasskey(p Passkey) {
	v.mu.Lock()
	v.lock.Passkeys, v.active = append(v.lock.Passkeys, p), time.Now()
	v.mu.Unlock()
}

// usedPasskey writes back the credential an unlock returned, whose
// counter has moved.
func (v *Visit) usedPasskey(credential webauthn.Credential, now time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := range v.lock.Passkeys {
		if bytes.Equal(v.lock.Passkeys[i].Credential.ID, credential.ID) {
			v.lock.Passkeys[i].Credential = credential
			v.lock.Passkeys[i].LastUsed = now.UTC()
		}
	}
}

// lockOpen says which paths a locked visit still answers: what any
// browser may fetch, and the way to unlock or leave.
func lockOpen(path string) bool {
	return isPublic(path) || path == unlockPath || strings.HasPrefix(path, unlockPath+"/")
}

// unlockPath is the page a locked visit is sent to.
const unlockPath = "/unlock"

// unaskedHeader is on what a page asks for by itself, as when mail
// arrives. Only the reader's own browser sends it, and all it can do is
// bring the lock nearer.
const unaskedHeader = "Alborz-Unasked"

// isBackground says which requests a page makes by itself. They are not
// the reader doing something, or an open tab would never lock.
func isBackground(r *http.Request) bool {
	return r.URL.Path == "/events" || isPublic(r.URL.Path) || r.Header.Get(unaskedHeader) != ""
}

func redirectToUnlock(ctx *Context) error {
	to := unlockPath
	if ctx.Request().Method == http.MethodGet {
		to += "?next=" + url.QueryEscape(ctx.Request().URL.RequestURI())
	}
	return ctx.Redirect(http.StatusSeeOther, to)
}

// passkeyUser is the visit as WebAuthn sees a user. The handle is the
// visit's id: a passkey opens this browser's visit and nothing else.
type passkeyUser struct {
	id   string
	name string
	lock Lock
}

func (u passkeyUser) WebAuthnID() []byte          { return []byte(u.id) }
func (u passkeyUser) WebAuthnName() string        { return u.name }
func (u passkeyUser) WebAuthnDisplayName() string { return u.name }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, len(u.lock.Passkeys))
	for i, p := range u.lock.Passkeys {
		out[i] = p.Credential
	}
	return out
}

// passkeys is the relying party for this request: the host the reader
// is on, which is the only one a passkey made here will answer to.
func (ctx *Context) passkeys() (*webauthn.WebAuthn, passkeyUser, error) {
	v := ctx.Visit()
	host := ctx.Request().Host
	id := host
	if h, _, ok := strings.Cut(host, ":"); ok {
		id = h
	}
	w, err := webauthn.New(&webauthn.Config{
		RPDisplayName: BrandName,
		RPID:          id,
		RPOrigins:     []string{ctx.Scheme() + "://" + host},
	})
	name := BrandName
	if accounts := ctx.Accounts(); len(accounts) > 0 {
		name = accounts[0].Username
	}
	return w, passkeyUser{id: v.ID, name: name, lock: v.Lock()}, err
}

// userVerified asks the authenticator for the reader, not only the
// device: a fingerprint, a face or a PIN is the whole of the lock.
var userVerified = protocol.VerificationRequired

// BeginPasskey starts registering a passkey and returns what the
// browser is to be asked.
func (ctx *Context) BeginPasskey() (*protocol.CredentialCreation, error) {
	w, user, err := ctx.passkeys()
	if err != nil {
		return nil, err
	}
	options, ceremony, err := w.BeginRegistration(user,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{UserVerification: userVerified}),
		webauthn.WithExclusions(webauthn.Credentials(user.WebAuthnCredentials()).CredentialDescriptors()))
	if err != nil {
		return nil, err
	}
	ctx.Visit().setCeremony(ceremony)
	return options, nil
}

// FinishPasskey reads the browser's answer and keeps the new passkey
// under the name given.
func (ctx *Context) FinishPasskey(name string) error {
	w, user, err := ctx.passkeys()
	if err != nil {
		return err
	}
	v := ctx.Visit()
	ceremony := v.takeCeremony()
	if ceremony == nil {
		return protocol.ErrBadRequest.WithDetails("no registration was started")
	}
	credential, err := w.FinishRegistration(user, *ceremony, ctx.Request())
	if err != nil {
		return err
	}
	v.addPasskey(Passkey{Name: name, Added: time.Now().UTC(), Credential: *credential})
	return ctx.Server.Visits.Save(v)
}

// BeginUnlock starts an assertion by one of the visit's passkeys.
func (ctx *Context) BeginUnlock() (*protocol.CredentialAssertion, error) {
	w, user, err := ctx.passkeys()
	if err != nil {
		return nil, err
	}
	options, ceremony, err := w.BeginLogin(user, webauthn.WithUserVerification(userVerified))
	if err != nil {
		return nil, err
	}
	ctx.Visit().setCeremony(ceremony)
	return options, nil
}

// FinishUnlock checks the assertion and opens the visit. The passkey's
// counter moves with each use, which is how a cloned one is noticed, so
// it is written back.
func (ctx *Context) FinishUnlock() error {
	w, user, err := ctx.passkeys()
	if err != nil {
		return err
	}
	v := ctx.Visit()
	ceremony := v.takeCeremony()
	if ceremony == nil {
		return protocol.ErrBadRequest.WithDetails("no unlock was started")
	}
	credential, err := w.FinishLogin(user, *ceremony, ctx.Request())
	if err != nil {
		return err
	}
	// A counter that did not move past the one kept is a second copy of
	// the passkey in use, which the library marks and lets through.
	if credential.Authenticator.CloneWarning {
		return fmt.Errorf("passkey %s: its counter did not advance, as a copied one's does not", base64.RawURLEncoding.EncodeToString(credential.ID))
	}
	now := time.Now()
	v.usedPasskey(*credential, now)
	v.unlock(now)
	return ctx.Server.Visits.Save(v)
}

func (v *Visit) setCeremony(c *webauthn.SessionData) {
	v.mu.Lock()
	v.ceremony = c
	v.mu.Unlock()
}

func (v *Visit) takeCeremony() *webauthn.SessionData {
	v.mu.Lock()
	defer v.mu.Unlock()
	c := v.ceremony
	v.ceremony = nil
	return c
}

// EndVisit signs every account out and forgets what the browser asked
// to be remembered: the way out of a lock whose passkey is gone.
func (ctx *Context) EndVisit() {
	v := ctx.lookupVisit()
	if v == nil {
		return
	}
	live, _ := v.Accounts()
	for _, s := range live {
		ctx.Server.ForgetAccount(s.Username())
		v.Remove(s.Username())
		s.Close()
	}
	ctx.Server.Visits.Delete(v.ID)
	ctx.SetCookie(ctx.cookie(visitCookieName, "", 0))
	ctx.visit, ctx.Session, ctx.DefaultSession = nil, nil, nil
}
