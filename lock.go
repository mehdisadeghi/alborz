package alborz

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// DefaultLockAfter is how long a session with a passkey may idle before it locks.
const DefaultLockAfter = loginDuration

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

// RemovePasskey removes one passkey from the session; false when none has that id.
func (s *Session) RemovePasskey(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.lock.Passkeys {
		if p.ID() == id {
			s.lock.Passkeys = append(s.lock.Passkeys[:i:i], s.lock.Passkeys[i+1:]...)
			return true
		}
	}
	return false
}

// SetLockAfter changes how long the session waits before it locks.
func (s *Session) SetLockAfter(d time.Duration) {
	s.mu.Lock()
	s.lock.After = d
	s.mu.Unlock()
}

// Lock holds a session's passkeys and how long it may idle before they are asked for.
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

// Lock is the session's lock as it stands.
func (s *Session) Lock() Lock {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Lock{Passkeys: append([]Passkey(nil), s.lock.Passkeys...), After: s.lock.After}
}

func (s *Session) locks() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lock.Passkeys) > 0
}

// Locked reports whether the session wants its passkey; a session rebuilt after a restart starts locked.
func (s *Session) Locked(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lock.Passkeys) == 0 {
		return false
	}
	if now.Sub(s.active) > s.lock.Wait() {
		s.locked = true
	}
	return s.locked
}

func (s *Session) act(now time.Time) {
	s.mu.Lock()
	s.active = now
	s.mu.Unlock()
}

// LockNow locks the session as if the lock time had run out.
func (s *Session) LockNow() {
	s.mu.Lock()
	s.locked = true
	s.mu.Unlock()
}

func (s *Session) unlock(now time.Time) {
	s.mu.Lock()
	s.locked, s.active = false, now
	s.mu.Unlock()
}

// Registering the first passkey counts as activity so it does not lock at once.
func (s *Session) addPasskey(p Passkey) {
	s.mu.Lock()
	s.lock.Passkeys, s.active = append(s.lock.Passkeys, p), time.Now()
	s.mu.Unlock()
}

func (s *Session) usedPasskey(credential webauthn.Credential, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.lock.Passkeys {
		if bytes.Equal(s.lock.Passkeys[i].Credential.ID, credential.ID) {
			s.lock.Passkeys[i].Credential = credential
			s.lock.Passkeys[i].LastUsed = now.UTC()
		}
	}
}

func lockOpen(path string) bool {
	return isPublic(path) || path == unlockPath || strings.HasPrefix(path, unlockPath+"/")
}

const unlockPath = "/unlock"

// unaskedHeader marks a request a page makes by itself; sending it can only bring the lock nearer.
const unaskedHeader = "Alborz-Unasked"

// Background requests are not the reader acting, or an open tab would never lock.
func isBackground(r *http.Request) bool {
	return r.URL.Path == "/events" || isPublic(r.URL.Path) || r.Header.Get(unaskedHeader) != ""
}

func redirectToUnlock(ctx *Context) error {
	to := unlockPath
	if ctx.Request().Method == http.MethodGet {
		to += "?next=" + QueryValue(ctx.Request().URL.RequestURI())
	}
	return ctx.Redirect(http.StatusSeeOther, to)
}

// The user handle is the session id, so a passkey opens only this browser's session.
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

func (ctx *Context) passkeys() (*webauthn.WebAuthn, passkeyUser, error) {
	s := ctx.Session()
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
	return w, passkeyUser{id: s.ID, name: name, lock: s.Lock()}, err
}

// User verification is required: the authenticator is the whole lock.
var userVerified = protocol.VerificationRequired

// BeginPasskey starts registering a passkey.
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
	ctx.Session().setCeremony(ceremony)
	return options, nil
}

// FinishPasskey keeps the passkey the browser registered under the given name.
func (ctx *Context) FinishPasskey(name string) error {
	w, user, err := ctx.passkeys()
	if err != nil {
		return err
	}
	s := ctx.Session()
	ceremony := s.takeCeremony()
	if ceremony == nil {
		return protocol.ErrBadRequest.WithDetails("no registration was started")
	}
	credential, err := w.FinishRegistration(user, *ceremony, ctx.Request())
	if err != nil {
		return err
	}
	s.addPasskey(Passkey{Name: name, Added: time.Now().UTC(), Credential: *credential})
	return ctx.Server.Sessions.Save(s)
}

// BeginUnlock starts an assertion by one of the session's passkeys.
func (ctx *Context) BeginUnlock() (*protocol.CredentialAssertion, error) {
	w, user, err := ctx.passkeys()
	if err != nil {
		return nil, err
	}
	options, ceremony, err := w.BeginLogin(user, webauthn.WithUserVerification(userVerified))
	if err != nil {
		return nil, err
	}
	ctx.Session().setCeremony(ceremony)
	return options, nil
}

// FinishUnlock checks the assertion, opens the session and stores the moved counter.
func (ctx *Context) FinishUnlock() error {
	w, user, err := ctx.passkeys()
	if err != nil {
		return err
	}
	s := ctx.Session()
	ceremony := s.takeCeremony()
	if ceremony == nil {
		return protocol.ErrBadRequest.WithDetails("no unlock was started")
	}
	credential, err := w.FinishLogin(user, *ceremony, ctx.Request())
	if err != nil {
		return err
	}
	// The library only flags a counter that did not advance; refuse it here.
	if credential.Authenticator.CloneWarning {
		return fmt.Errorf("passkey %s: its counter did not advance, as a copied one's does not", base64.RawURLEncoding.EncodeToString(credential.ID))
	}
	now := time.Now()
	s.usedPasskey(*credential, now)
	s.unlock(now)
	return ctx.Server.Sessions.Save(s)
}

func (s *Session) setCeremony(c *webauthn.SessionData) {
	s.mu.Lock()
	s.ceremony = c
	s.mu.Unlock()
}

func (s *Session) takeCeremony() *webauthn.SessionData {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.ceremony
	s.ceremony = nil
	return c
}

// EndSession signs every account out and forgets what the browser asked to be remembered.
func (ctx *Context) EndSession() {
	session := ctx.lookupSession()
	if session == nil {
		return
	}
	live, _ := session.Accounts()
	for _, s := range live {
		ctx.Server.ForgetAccount(s.Username())
		session.Remove(s.Username())
		s.Close()
	}
	if err := ctx.Server.Sessions.Delete(session.ID); err != nil {
		ctx.Logger().Printf("failed to forget the session: %v", err)
	}
	ctx.SetCookie(ctx.cookie(sessionCookieName, "", 0))
	ctx.session, ctx.Login, ctx.DefaultLogin = nil, nil, nil
}
