package alborz

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/fernet/fernet-go"
	"github.com/go-webauthn/webauthn/webauthn"
)

// A Session is one browser's stay, holding what belongs to the person at the
// screen rather than to any one account.
type Session struct {
	ID      string
	created time.Time

	mu          sync.Mutex
	seen        time.Time
	notice      *Notice
	attachments map[string]*Attachment
	// accounts is every account this browser has signed into, in display order.
	accounts []*Login
	// proof verifies the browser's secret; only the secret opens the session.
	proof []byte
	// remember holds each password sealed under the browser's secret.
	remember map[string][]byte
	profile  *Profile
	// said holds conditions already told once.
	said            map[string]bool
	lock            Lock
	active          time.Time
	locked          bool
	ceremony        *webauthn.SessionData
	device, address string
}

func (s *Session) Profile() (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.profile == nil {
		return Profile{}, false
	}
	return *s.profile, true
}

func (s *Session) setProfile(p Profile) {
	s.mu.Lock()
	s.profile = &p
	s.mu.Unlock()
}

func (s *Session) life() time.Duration {
	if s.remembered() {
		return credentialCookieLife
	}
	return sessionDuration
}

func (s *Session) remembered() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.remember) > 0
}

// Remember keeps the account's password sealed under the browser's secret.
func (s *Session) Remember(address string, sealed []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remember == nil {
		s.remember = map[string][]byte{}
	}
	s.remember[address] = sealed
}

func (s *Session) forget(address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.remember, address)
}

// record keeps only what means something after a restart.
func (s *Session) record() *sessionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := &sessionRecord{ID: s.ID, Created: s.created, Seen: s.seen, Proof: s.proof,
		Profile: s.profile, Lock: s.lock, Device: s.device, Address: s.address}
	for address, sealed := range s.remember {
		rec.Accounts = append(rec.Accounts, rememberedAccount{Address: address, Sealed: sealed})
	}
	slices.SortFunc(rec.Accounts, func(a, b rememberedAccount) int {
		return strings.Compare(a.Address, b.Address)
	})
	return rec
}

func sessionFromRecord(rec *sessionRecord) *Session {
	s := &Session{
		ID:          rec.ID,
		created:     rec.Created,
		proof:       rec.Proof,
		seen:        time.Now(),
		attachments: map[string]*Attachment{},
		remember:    map[string][]byte{},
	}
	for _, a := range rec.Accounts {
		s.remember[a.Address] = a.Sealed
	}
	s.profile, s.lock = rec.Profile, rec.Lock
	s.device, s.address = rec.Device, rec.Address
	return s
}

// Accounts returns the live logins and the addresses of ended ones.
func (s *Session) Accounts() (live []*Login, lost []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.accounts[:0]
	for _, login := range s.accounts {
		if login.alive() {
			kept = append(kept, login)
			continue
		}
		lost = append(lost, login.Username())
	}
	s.accounts = kept
	return slices.Clone(s.accounts), lost
}

// add puts an account in the session, replacing an earlier login for it.
func (s *Session) add(login *Login) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, other := range s.accounts {
		if other.username == login.username {
			other.close()
			s.accounts[i] = login
			byName(s.accounts)
			return
		}
	}
	s.accounts = append(s.accounts, login)
	byName(s.accounts)
}

// remove takes an account out of the session and closes it.
func (s *Session) remove(username string) *Login {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, login := range s.accounts {
		if login.username == username {
			s.accounts = append(s.accounts[:i], s.accounts[i+1:]...)
			return login
		}
	}
	return nil
}

// sessionCookieName carries only the id and the secret; the state stays on the server.
const sessionCookieName = "alborz_session"

// sessionRecord is a session kept across a restart.
type sessionRecord struct {
	ID       string
	Created  time.Time
	Seen     time.Time
	Proof    []byte
	Accounts []rememberedAccount
	Profile  *Profile
	Lock     Lock
	Device   string `json:",omitempty"`
	Address  string `json:",omitempty"`
}

// rememberedAccount is one account of a remembered session.
type rememberedAccount struct {
	Address string
	Sealed  []byte
}

// sessionRecords keeps remembered sessions.
type sessionRecords interface {
	Load(id string) (*sessionRecord, bool)
	Save(rec *sessionRecord) error
	Delete(id string) error
	Sweep(before time.Time) error
	Each(func(*sessionRecord)) error
}

// Sessions are the browsers signed in.
type Sessions struct {
	mu      sync.Mutex
	live    map[string]*Session
	records sessionRecords
}

func newSessions(records sessionRecords) *Sessions {
	return &Sessions{live: map[string]*Session{}, records: records}
}

func (ss *Sessions) get(id string) *Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s := ss.live[id]
	if s == nil {
		return nil
	}
	if time.Since(s.lastSeen()) > s.life() {
		delete(ss.live, id)
		return nil
	}
	return s
}

func (ss *Sessions) put(s *Session) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.live[s.ID] = s
}

func (ss *Sessions) delete(id string) error {
	ss.mu.Lock()
	s := ss.live[id]
	delete(ss.live, id)
	ss.mu.Unlock()
	if s != nil {
		s.close()
	}
	return ss.records.Delete(id)
}

// record returns the remembered session of that id.
func (ss *Sessions) record(id string) (*sessionRecord, bool) {
	return ss.records.Load(id)
}

// Save writes a remembered session; others are never written.
func (ss *Sessions) Save(s *Session) error {
	if !s.remembered() {
		return nil
	}
	return ss.records.Save(s.record())
}

// sweep drops the records nobody has come back to.
func (ss *Sessions) sweep(before time.Time) error {
	return ss.records.Sweep(before)
}

// sessionDuration matches the login's so nothing changes hands while signed in.
const sessionDuration = loginDuration

func newSession() (*Session, string) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	now := time.Now()
	secret := base64.RawURLEncoding.EncodeToString(b[32:])
	s := &Session{
		ID:          base64.RawURLEncoding.EncodeToString(b[:32]),
		created:     now,
		proof:       sessionProof(secret),
		seen:        now,
		active:      now,
		attachments: map[string]*Attachment{},
	}
	return s, secret
}

func (s *Session) lastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen
}

func (s *Session) touch() {
	s.mu.Lock()
	s.seen = time.Now()
	s.mu.Unlock()
}

// Once reports whether this is the first time the session hears of key.
func (s *Session) Once(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.said[key] {
		return false
	}
	if s.said == nil {
		s.said = map[string]bool{}
	}
	s.said[key] = true
	return true
}

// notify records what the next page tells the reader.
func (s *Session) notify(n Notice) {
	// An empty notice means the action speaks for itself.
	if n.Text == "" && n.Markup == "" {
		return
	}
	s.mu.Lock()
	s.notice = &n
	s.mu.Unlock()
}

// hasNotice reports whether a notice waits for the next page.
func (s *Session) hasNotice() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notice != nil
}

// popNotice takes the pending notice, once.
func (s *Session) popNotice() *Notice {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.notice
	s.notice = nil
	return n
}

// putAttachment keeps a file the compose form has taken but not sent.
func (s *Session) putAttachment(in *multipart.FileHeader, form *multipart.Form) (string, error) {
	id := uuid.New()
	s.mu.Lock()
	defer s.mu.Unlock()

	var size int64
	for _, a := range s.attachments {
		size += a.File.Size
	}
	if size+in.Size > MaxAttachmentSize {
		return "", ErrAttachmentCacheSize
	}
	s.attachments[id.String()] = &Attachment{File: in, Form: form}
	return id.String(), nil
}

// peekAttachment leaves the attachment in place, so a refused send finds it again.
func (s *Session) peekAttachment(id string) *Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attachments[id]
}

// popAttachment takes an attachment back, once.
func (s *Session) popAttachment(id string) *Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attachments[id]
	if !ok {
		return nil
	}
	delete(s.attachments, id)
	return a
}

func (s *Session) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.attachments {
		if a.Form != nil {
			a.Form.RemoveAll()
		}
	}
	s.attachments = map[string]*Attachment{}
}

// Session returns this browser's session, made on first use.
func (ctx *Context) Session() *Session {
	if s := ctx.lookupSession(); s != nil {
		return s
	}
	s, secret := newSession()
	ctx.Server.Sessions.put(s)
	ctx.SetCookie(ctx.cookie(sessionCookieName, s.ID+"."+secret, credentialCookieLife))
	ctx.session, ctx.sessionSecret = s, secret
	return s
}

// lookupSession finds this browser's session without making one. The id is
// shown to other browsers of the account, so only the secret opens it.
func (ctx *Context) lookupSession() *Session {
	if ctx.session != nil {
		return ctx.session
	}
	value, ok := ctx.cookieValue(sessionCookieName, func(string) bool { return true })
	if !ok {
		return nil
	}
	id, secret, _ := strings.Cut(value, ".")
	s := ctx.Server.Sessions.get(id)
	stored := s == nil
	if stored {
		rec, found := ctx.Server.Sessions.record(id)
		if !found {
			return nil
		}
		s = sessionFromRecord(rec)
	}
	if !s.opens(secret) {
		return nil
	}
	if stored {
		ctx.Server.Sessions.put(s)
	}
	s.touch()
	ctx.session, ctx.sessionSecret = s, secret
	return s
}

// sessionProofLabel keeps the proof apart from sessionKey, or the store could
// open the passwords it holds.
const sessionProofLabel = "alborz session proof"

func sessionProof(secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sessionProofLabel))
	return mac.Sum(nil)
}

// opens reports whether secret is this session's.
func (s *Session) opens(secret string) bool {
	return len(s.proof) > 0 && hmac.Equal(s.proof, sessionProof(secret))
}

// sessionKey derives the sealing key; the secret never leaves the cookie, so a
// stolen store decrypts nothing.
func sessionKey(secret string) *fernet.Key {
	sum := sha256.Sum256([]byte(secret))
	var key fernet.Key
	copy(key[:], sum[:])
	return &key
}

func (ctx *Context) seal(password string) ([]byte, error) {
	if ctx.sessionSecret == "" {
		return nil, fmt.Errorf("alborz: this session carries no secret")
	}
	return fernet.EncryptAndSign([]byte(password), sessionKey(ctx.sessionSecret))
}

func (ctx *Context) unseal(sealed []byte) (string, bool) {
	if ctx.sessionSecret == "" {
		return "", false
	}
	raw := fernet.VerifyAndDecrypt(sealed, credentialCookieLife, []*fernet.Key{sessionKey(ctx.sessionSecret)})
	if raw == nil {
		return "", false
	}
	return string(raw), true
}

// Quiet says this action needs no notice. Said explicitly, so a forgotten
// notice is told apart from a chosen silence.
func (ctx *Context) Quiet() { ctx.quiet = true }

// Notify records what the next page tells the reader.
func (ctx *Context) Notify(n Notice) {
	// An empty notice is Quiet by another name.
	if n.Text == "" && n.Markup == "" {
		ctx.Quiet()
		return
	}
	ctx.Session().notify(n)
}

// PutNotice says a request did what it was asked.
func (ctx *Context) PutNotice(text string) {
	ctx.Notify(Notice{Kind: NoticeDone, Text: text})
}

func (ctx *Context) popNotice() *Notice {
	if s := ctx.lookupSession(); s != nil {
		return s.popNotice()
	}
	return nil
}

// PutAttachment holds a file the compose form has taken but not sent.
func (ctx *Context) PutAttachment(in *multipart.FileHeader, form *multipart.Form) (string, error) {
	return ctx.Session().putAttachment(in, form)
}

func (ctx *Context) PeekAttachment(id string) *Attachment {
	if s := ctx.lookupSession(); s != nil {
		return s.peekAttachment(id)
	}
	return nil
}

func (ctx *Context) PopAttachment(id string) *Attachment {
	if s := ctx.lookupSession(); s != nil {
		return s.popAttachment(id)
	}
	return nil
}
