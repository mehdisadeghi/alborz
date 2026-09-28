package alborz

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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

// A Session is one browser's stay: the accounts it has signed into and
// what the reader is owed. It exists because a notice and an unsent
// upload belong to the person at the screen and to no account, and
// filing them under one account is how a notice raised on a page
// scoped to it turns up later on something else.
//
// The login holds what IMAP forces, a credential and a connection.
// Everything the reader would recognise as theirs lives here.
type Session struct {
	ID      string
	Created time.Time

	mu          sync.Mutex
	seen        time.Time
	notice      *Notice
	attachments map[string]*Attachment
	// accounts is the bag: every account this browser has signed into,
	// in one order wherever they are listed.
	accounts []*Login
	// proof is what the session keeps of the secret its browser carries:
	// the id names the session, and only the secret opens it.
	proof []byte
	// remember holds each account's password sealed under the secret
	// the browser carries, for a session the reader asked to keep.
	remember map[string][]byte
	// read is what the pages are read by, and anchor the account it is
	// kept on. A nil read means the browser has chosen nothing yet and
	// will take what the first account brings.
	read   *Reading
	anchor string
	// said holds what this browser has been told and need not hear on
	// every page: a standing condition is news once.
	said map[string]bool
	// lock is what stands in front of the session, active when the reader
	// last did something, and ceremony the WebAuthn exchange under way.
	lock     Lock
	active   time.Time
	locked   bool
	ceremony *webauthn.SessionData
	// device and address are what the browser last said about itself,
	// for the account holder's list of browsers signed in.
	device, address string
}

func (s *Session) reading() Reading {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.read == nil {
		return Reading{}
	}
	return *s.read
}

func (s *Session) hasReading() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read != nil
}

func (s *Session) setReading(r Reading) {
	s.mu.Lock()
	s.read = &r
	s.mu.Unlock()
}

// Anchor is the account this browser's reading settings are kept on,
// empty when they are kept nowhere.
func (s *Session) Anchor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.anchor
}

func (s *Session) setAnchor(account string) {
	s.mu.Lock()
	s.anchor = account
	s.mu.Unlock()
}

// life is how long the session outlives its last request: as long as a
// login when nobody asked to be remembered, and as long as the
// credentials are worth keeping when they did.
func (s *Session) life() time.Duration {
	if s.remembered() {
		return credentialCookieLife
	}
	return sessionDuration
}

// remembered reports whether the reader asked this browser to stay
// signed in.
func (s *Session) remembered() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.remember) > 0
}

// Remember keeps the account's password for this session, sealed under
// the browser's own secret, so a restart costs a reconnection rather
// than a login. Forgetting it is signing out.
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

// record is the session as it is written down: no notice, no uploads,
// nothing that would mean anything after a restart.
func (s *Session) record() *SessionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := &SessionRecord{ID: s.ID, Created: s.Created, Seen: s.seen, Proof: s.proof,
		Reading: s.read, Anchor: s.anchor, Lock: s.lock, Device: s.device, Address: s.address}
	for address, sealed := range s.remember {
		rec.Accounts = append(rec.Accounts, RememberedAccount{Address: address, Sealed: sealed})
	}
	slices.SortFunc(rec.Accounts, func(a, b RememberedAccount) int {
		return strings.Compare(a.Address, b.Address)
	})
	return rec
}

// sessionFromRecord rebuilds a remembered session, with no accounts open
// yet: the logins are made when the first request needs them.
func sessionFromRecord(rec *SessionRecord) *Session {
	s := &Session{
		ID:          rec.ID,
		Created:     rec.Created,
		proof:       rec.Proof,
		seen:        time.Now(),
		attachments: map[string]*Attachment{},
		remember:    map[string][]byte{},
	}
	for _, a := range rec.Accounts {
		s.remember[a.Address] = a.Sealed
	}
	s.read, s.anchor, s.lock = rec.Reading, rec.Anchor, rec.Lock
	s.device, s.address = rec.Device, rec.Address
	return s
}

// Accounts is the bag, with the accounts whose login has ended left
// out and named, so the page can say who was signed out rather than
// pretending they are still here.
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

// Add puts an account in the bag, replacing an earlier login for the
// same address, and keeps the bag in one order.
func (s *Session) Add(login *Login) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, other := range s.accounts {
		if other.username == login.username {
			other.Close()
			s.accounts[i] = login
			byName(s.accounts)
			return
		}
	}
	s.accounts = append(s.accounts, login)
	byName(s.accounts)
}

// Remove takes an account out of the bag and closes it.
func (s *Session) Remove(username string) *Login {
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

// sessionCookieName carries the id and the secret and nothing else: what
// the session holds is the server's business, and the browser only has to
// say which one it is and show that it is its own.
const sessionCookieName = "alborz_session"

// SessionRecord is what a session is worth keeping across a restart: who
// it has signed into, and the passwords to sign in again, each sealed
// under a key only the browser's cookie carries.
type SessionRecord struct {
	ID       string
	Created  time.Time
	Seen     time.Time
	Proof    []byte
	Accounts []RememberedAccount
	Reading  *Reading
	Anchor   string
	Lock     Lock
	Device   string `json:",omitempty"`
	Address  string `json:",omitempty"`
}

// RememberedAccount is one account of a remembered session.
type RememberedAccount struct {
	Address string
	Sealed  []byte
}

// SessionRecords is where remembered sessions live. Nothing is written
// until a reader asks to be remembered, and a deployment without a
// store simply cannot offer it. A bbolt file implements this today; a
// Redis or SQL one would implement the same four methods.
type SessionRecords interface {
	Load(id string) (*SessionRecord, bool)
	Save(rec *SessionRecord) error
	Delete(id string) error
	Sweep(before time.Time) error
	// Each hands over every session kept, for the list of browsers an
	// account is signed into.
	Each(func(*SessionRecord)) error
	// LoadReading and SaveReading keep what a person reads by on the
	// account they anchored it to, which is the only identity alborz
	// has to hang it on.
	LoadReading(account string) (*Reading, bool)
	SaveReading(account string, r *Reading) error
	DeleteReading(account string) error
	// LoadSettings and SaveSettings hold what an account's own server will
	// not: without METADATA there is nowhere on it to write, and the
	// settings would otherwise last only as long as the process.
	LoadSettings(account string) (map[string]json.RawMessage, bool)
	SaveSettings(account string, entries map[string]json.RawMessage) error
}

// Sessions are the browsers signed in. The live ones are held here, and
// the remembered ones are also written to the records, so a restart
// costs a reconnection rather than a login.
type Sessions struct {
	mu      sync.Mutex
	live    map[string]*Session
	records SessionRecords
}

func newSessions() *Sessions {
	return &Sessions{live: map[string]*Session{}}
}

// SetRecords makes remembering possible. Without one, a session lasts as
// long as the process.
func (ss *Sessions) SetRecords(r SessionRecords) { ss.records = r }

// Remembers reports whether this deployment can remember a session.
func (ss *Sessions) Remembers() bool { return ss.records != nil }

// Records is where remembered sessions and the settings of an account
// whose server has no METADATA are written, nil when this deployment
// has nowhere of its own to write.
func (ss *Sessions) Records() SessionRecords { return ss.records }

func (ss *Sessions) Get(id string) *Session {
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

func (ss *Sessions) Put(s *Session) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.live[s.ID] = s
}

func (ss *Sessions) Delete(id string) {
	ss.mu.Lock()
	s := ss.live[id]
	delete(ss.live, id)
	ss.mu.Unlock()
	if s != nil {
		s.close()
	}
	if ss.records != nil {
		ss.records.Delete(id)
	}
}

// Record is the remembered session of that id, when there is one.
func (ss *Sessions) Record(id string) (*SessionRecord, bool) {
	if ss.records == nil {
		return nil, false
	}
	return ss.records.Load(id)
}

// Save writes what a remembered session is made of. A session nobody asked
// to remember is never written.
func (ss *Sessions) Save(s *Session) error {
	if ss.records == nil || !s.remembered() {
		return nil
	}
	return ss.records.Save(s.record())
}

// LoadReading and SaveReading are what an account keeps for whoever
// anchored their reading settings to it.
func (ss *Sessions) LoadReading(account string) (*Reading, bool) {
	if ss.records == nil {
		return nil, false
	}
	return ss.records.LoadReading(account)
}

// ForgetReading drops what is kept under an account, for a reader
// taking back everything alborz wrote about them there.
func (ss *Sessions) ForgetReading(account string) error {
	if ss.records == nil {
		return nil
	}
	return ss.records.DeleteReading(account)
}

func (ss *Sessions) SaveReading(account string, r Reading) error {
	if ss.records == nil {
		return nil
	}
	return ss.records.SaveReading(account, &r)
}

// Sweep drops the records nobody has come back to.
func (ss *Sessions) Sweep(before time.Time) {
	if ss.records != nil {
		ss.records.Sweep(before)
	}
}

// sessionDuration is how long a session outlives its last request. It
// matches the login's, so nothing changes hands while a reader is
// signed in; a persisted session will outlive both.
const sessionDuration = loginDuration

// newSession makes a session and the secret its browser keeps.
func newSession() (*Session, string) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		panic(err) // A machine that cannot make a random id cannot serve mail.
	}
	now := time.Now()
	secret := base64.RawURLEncoding.EncodeToString(b[32:])
	s := &Session{
		ID:          base64.RawURLEncoding.EncodeToString(b[:32]),
		Created:     now,
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

// Notify records what the next page tells the reader.
func (s *Session) Notify(n Notice) {
	// A notice with nothing to say is a handler deciding this action
	// speaks for itself; an empty bar would say the opposite.
	if n.Text == "" && n.Markup == "" {
		return
	}
	s.mu.Lock()
	s.notice = &n
	s.mu.Unlock()
}

// HasNotice reports whether a notice waits for the next page.
func (s *Session) HasNotice() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notice != nil
}

// PopNotice takes what the reader is owed, once.
func (s *Session) PopNotice() *Notice {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.notice
	s.notice = nil
	return n
}

// PutAttachment keeps a file the compose form has taken but not sent.
func (s *Session) PutAttachment(in *multipart.FileHeader, form *multipart.Form) (string, error) {
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

// PeekAttachment is one the form still holds, left in place: a send the
// server refuses must find it again.
func (s *Session) PeekAttachment(id string) *Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attachments[id]
}

// PopAttachment takes one back, once.
func (s *Session) PopAttachment(id string) *Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attachments[id]
	if !ok {
		return nil
	}
	delete(s.attachments, id)
	return a
}

// close drops what the session was holding for a reader who is leaving.
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

// Session is this browser's stay, made on first use. A request that
// stores nothing never makes one, so a crawler and the login page cost
// nothing.
func (ctx *Context) Session() *Session {
	if s := ctx.lookupSession(); s != nil {
		return s
	}
	s, secret := newSession()
	ctx.Server.Sessions.Put(s)
	ctx.SetCookie(ctx.cookie(sessionCookieName, s.ID+"."+secret, credentialCookieLife))
	ctx.session, ctx.sessionSecret = s, secret
	return s
}

// lookupSession finds this browser's stay without making one. The cookie
// carries the id and a secret: the id says which session, and is shown to
// every other browser of the same account, so it opens nothing; the
// secret is what makes the session this browser's, and the only key to
// the passwords a remembered session holds.
func (ctx *Context) lookupSession() *Session {
	if ctx.session != nil {
		return ctx.session
	}
	value, ok := ctx.cookieValue(sessionCookieName, func(string) bool { return true })
	if !ok {
		return nil
	}
	id, secret, _ := strings.Cut(value, ".")
	s := ctx.Server.Sessions.Get(id)
	stored := s == nil
	if stored {
		rec, found := ctx.Server.Sessions.Record(id)
		if !found {
			return nil
		}
		s = sessionFromRecord(rec)
	}
	if !s.opens(secret) {
		return nil
	}
	if stored {
		ctx.Server.Sessions.Put(s)
	}
	s.touch()
	ctx.session, ctx.sessionSecret = s, secret
	return s
}

// sessionProofLabel keeps the proof apart from sessionKey, which is made of
// the same secret: a proof that was the key would let the store open
// the passwords it holds.
const sessionProofLabel = "alborz session proof"

// sessionProof is what a session keeps of its browser's secret: enough to
// know the secret when it is shown, and nothing that opens a password.
func sessionProof(secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sessionProofLabel))
	return mac.Sum(nil)
}

// opens reports whether secret is this session's. A session with no proof
// was written before sessions kept one, and nobody can show it is theirs.
func (s *Session) opens(secret string) bool {
	return len(s.proof) > 0 && hmac.Equal(s.proof, sessionProof(secret))
}

// sessionKey is the key the browser's secret stands for. The secret
// never leaves the cookie, so a stolen store decrypts nothing.
func sessionKey(secret string) *fernet.Key {
	sum := sha256.Sum256([]byte(secret))
	var key fernet.Key
	copy(key[:], sum[:])
	return &key
}

// seal locks a password to this browser, and unseal opens it again.
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

// Notify records what the next page tells the reader, and PutNotice
// says a request did what it was asked.
// Quiet says this action needs no notice: its result shows where the
// reader is. Said rather than left out, so a handler that forgot to
// speak is told apart from one that chose not to.
func (ctx *Context) Quiet() { ctx.quiet = true }

func (ctx *Context) Notify(n Notice) {
	// An empty notice is a handler deciding the action speaks for
	// itself, which is Quiet by another name.
	if n.Text == "" && n.Markup == "" {
		ctx.Quiet()
		return
	}
	ctx.Session().Notify(n)
}

func (ctx *Context) PutNotice(text string) {
	ctx.Notify(Notice{Kind: NoticeDone, Text: text})
}

func (ctx *Context) PopNotice() *Notice {
	if s := ctx.lookupSession(); s != nil {
		return s.PopNotice()
	}
	return nil
}

// PutAttachment, PeekAttachment and PopAttachment hold what the
// compose form has taken and not yet sent.
func (ctx *Context) PutAttachment(in *multipart.FileHeader, form *multipart.Form) (string, error) {
	return ctx.Session().PutAttachment(in, form)
}

func (ctx *Context) PeekAttachment(id string) *Attachment {
	if s := ctx.lookupSession(); s != nil {
		return s.PeekAttachment(id)
	}
	return nil
}

func (ctx *Context) PopAttachment(id string) *Attachment {
	if s := ctx.lookupSession(); s != nil {
		return s.PopAttachment(id)
	}
	return nil
}
