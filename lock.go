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

// DefaultLockAfter is how long a reader may do nothing before a session
// with a passkey asks for it again: long enough to read a long message
// and answer the door, short enough that a phone left behind is locked
// before it is found. It is a login's life, so the two ask together.
const DefaultLockAfter = loginDuration

// MinLockAfter keeps a typed lock time from locking between two clicks.
const MinLockAfter = time.Minute

// Passkey is a WebAuthn credential a browser registered for its session.
// WebAuthn lets a user hold several - the phone's own, a hardware key
// kept for when the phone is gone - so a session keeps a list.
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

// RemovePasskey takes one passkey off the session; false when it has no
// passkey of that id. Removing the last one removes the lock.
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

// Lock is what stands in front of a session: the passkeys that open it,
// and how long the reader may be away before they are asked for. A
// session with no passkey has no lock.
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

// locks reports whether the session has a passkey to lock behind.
func (s *Session) locks() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lock.Passkeys) > 0
}

// Locked reports whether the session wants its passkey, deciding it when
// the reader has been away too long. A session rebuilt after a restart
// has no activity on record and starts locked.
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

// act notes that the reader did something.
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

// addPasskey keeps a new passkey; registering the first is the reader
// being here, so it does not lock them out at once.
func (s *Session) addPasskey(p Passkey) {
	s.mu.Lock()
	s.lock.Passkeys, s.active = append(s.lock.Passkeys, p), time.Now()
	s.mu.Unlock()
}

// usedPasskey writes back the credential an unlock returned, whose
// counter has moved.
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

// lockOpen says which paths a locked session still answers: what any
// browser may fetch, and the way to unlock or leave.
func lockOpen(path string) bool {
	return isPublic(path) || path == unlockPath || strings.HasPrefix(path, unlockPath+"/")
}

// unlockPath is the page a locked session is sent to.
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
		to += "?next=" + QueryValue(ctx.Request().URL.RequestURI())
	}
	return ctx.Redirect(http.StatusSeeOther, to)
}

// passkeyUser is the session as WebAuthn sees a user. The handle is the
// session's id: a passkey opens this browser's session and nothing else.
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
	ctx.Session().setCeremony(ceremony)
	return options, nil
}

// FinishPasskey reads the browser's answer and keeps the new passkey
// under the name given.
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

// FinishUnlock checks the assertion and opens the session. The passkey's
// counter moves with each use, which is how a cloned one is noticed, so
// it is written back.
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
	// A counter that did not move past the one kept is a second copy of
	// the passkey in use, which the library marks and lets through.
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

// EndSession signs every account out and forgets what the browser asked
// to be remembered: the way out of a lock whose passkey is gone.
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
