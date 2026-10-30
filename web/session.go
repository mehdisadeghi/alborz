package web

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/seal"
	"github.com/go-webauthn/webauthn/webauthn"
)

// A Session is one browser's stay, holding what belongs to the person at the
// screen rather than to any one account.
type Session struct {
	ID      string
	created time.Time

	mu          sync.Mutex
	seen        time.Time
	expires     time.Time
	ended       bool
	notice      *Notice
	attachments map[string]*File
	// accounts is every account this browser has signed into, in display order.
	accounts []*alborz.Login
	// proof verifies the browser's secret; only the secret opens the session.
	proof []byte
	// remember holds each password sealed under the browser's secret.
	remember map[string][]byte
	profile  *alborz.Profile
	// pendingProfiles holds accounts whose settings the reader has not chosen yet.
	pendingProfiles map[string]bool
	// said holds conditions already told once.
	said            map[string]bool
	lock            alborz.Lock
	active          time.Time
	locked          bool
	ceremony        *webauthn.SessionData
	device, address string
}

func (s *Session) Profile() (alborz.Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.profile == nil {
		return alborz.Profile{}, false
	}
	return *s.profile, true
}

func (s *Session) setProfile(p alborz.Profile) {
	s.mu.Lock()
	s.profile = &p
	s.mu.Unlock()
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
func (s *Session) record() *alborz.SessionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recordLocked()
}

func (s *Session) recordLocked() *alborz.SessionRecord {
	rec := &alborz.SessionRecord{ID: s.ID, Created: s.created, Seen: s.seen, Proof: s.proof,
		Expires: s.expires, Profile: s.profile, Lock: s.lock, Device: s.device, Address: s.address}
	for address, sealed := range s.remember {
		rec.Accounts = append(rec.Accounts, alborz.RememberedAccount{Address: address, Sealed: sealed})
	}
	slices.SortFunc(rec.Accounts, func(a, b alborz.RememberedAccount) int {
		return strings.Compare(a.Address, b.Address)
	})
	return rec
}

func sessionFromRecord(rec *alborz.SessionRecord) *Session {
	s := &Session{
		ID:          rec.ID,
		created:     rec.Created,
		proof:       rec.Proof,
		seen:        rec.Seen,
		expires:     rec.Expires,
		attachments: map[string]*File{},
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
func (s *Session) Accounts() (live []*alborz.Login, lost []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.accounts[:0]
	for _, login := range s.accounts {
		if login.Alive() {
			kept = append(kept, login)
			continue
		}
		lost = append(lost, login.Username())
		delete(s.pendingProfiles, login.Username())
	}
	s.accounts = kept
	return slices.Clone(s.accounts), lost
}

// add puts an account in the session, replacing an earlier login for it.
func (s *Session) add(login *alborz.Login) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addLocked(login)
}

func (s *Session) addLocked(login *alborz.Login) {
	for i, other := range s.accounts {
		if other.Username() == login.Username() {
			other.Close()
			s.accounts[i] = login
			byName(s.accounts)
			return
		}
	}
	s.accounts = append(s.accounts, login)
	byName(s.accounts)
}

// restored commits only the credential that was read before upstream authentication began.
func (s *Session) restored(login *alborz.Login, sealed []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || !time.Now().Before(s.expires) ||
		!bytes.Equal(s.remember[login.Username()], sealed) {
		return false
	}
	if slices.ContainsFunc(s.accounts, func(other *alborz.Login) bool {
		return other.Username() == login.Username() && other.Alive()
	}) {
		return false
	}
	s.addLocked(login)
	return true
}

func (s *Session) forgetRefused(account string, sealed []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || !bytes.Equal(s.remember[account], sealed) {
		return false
	}
	delete(s.remember, account)
	return true
}

// remove revokes the account and its remembered credential together; the caller closes its login.
func (s *Session) remove(username string) *alborz.Login {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.remember, username)
	for i, login := range s.accounts {
		if login.Username() == username {
			s.accounts = append(s.accounts[:i], s.accounts[i+1:]...)
			delete(s.pendingProfiles, username)
			return login
		}
	}
	return nil
}

// sessionCookieName carries only the id and the secret; the state stays on the server.
const sessionCookieName = "alborz_session"

// Sessions are the browsers signed in.
type Sessions struct {
	mu      sync.Mutex
	live    map[string]*Session
	records alborz.SessionRecords
	// pushes are the devices a browser turned on, which signing it out ends.
	pushes *alborz.Pushes
}

func newSessions(records alborz.SessionRecords, pushes *alborz.Pushes) *Sessions {
	return &Sessions{live: map[string]*Session{}, records: records, pushes: pushes}
}

func (ss *Sessions) get(id string) *Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s := ss.live[id]
	if s == nil {
		return nil
	}
	if s.expired(time.Now()) {
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
	defer ss.mu.Unlock()
	s := ss.live[id]
	delete(ss.live, id)
	if s != nil {
		s.close()
	}
	return ss.records.Delete(id)
}

// record returns the remembered session of that id.
func (ss *Sessions) record(id string) (*alborz.SessionRecord, bool) {
	rec, ok := ss.records.Load(id)
	return rec, ok && time.Now().Before(rec.Expires)
}

func (s *Session) expired(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return true
	}
	if len(s.remember) == 0 {
		return now.Sub(s.seen) > sessionDuration
	}
	return !now.Before(s.expires)
}

// Save writes remembered credentials, or removes a record once none remain.
func (ss *Sessions) Save(s *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return nil
	}
	if len(s.remember) == 0 {
		return ss.records.Delete(s.ID)
	}
	return ss.records.Save(s.recordLocked())
}

// sessionDuration matches the login's so nothing changes hands while signed in.
const sessionDuration = alborz.LoginDuration

func newSession() (*Session, string) {
	b := make([]byte, 64)
	rand.Read(b)
	now := time.Now()
	secret := base64.RawURLEncoding.EncodeToString(b[32:])
	s := &Session{
		ID:          base64.RawURLEncoding.EncodeToString(b[:32]),
		created:     now,
		proof:       sessionProof(secret),
		seen:        now,
		active:      now,
		attachments: map[string]*File{},
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
func (s *Session) putAttachment(in *File) (string, error) {
	id := uuid.New()
	s.mu.Lock()
	defer s.mu.Unlock()

	var size int64
	for _, a := range s.attachments {
		size += a.Size()
	}
	if size+in.Size() > MaxAttachmentSize {
		return "", ErrAttachmentCacheSize
	}
	s.attachments[id.String()] = in
	return id.String(), nil
}

// peekAttachment leaves the attachment in place, so a refused send finds it again.
func (s *Session) peekAttachment(id string) *File {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attachments[id]
}

// popAttachment takes an attachment back, once.
func (s *Session) popAttachment(id string) *File {
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
	s.ended = true
	for _, a := range s.attachments {
		a.Close()
	}
	s.attachments = map[string]*File{}
}

// Session returns this browser's session, made on first use.
func (ctx *Context) Session() *Session {
	if s := ctx.lookupSession(); s != nil {
		return s
	}
	s, secret := newSession()
	ctx.Server.Sessions.put(s)
	ctx.SetCookie(ctx.cookie(sessionCookieName, s.ID+"."+secret, alborz.CredentialCookieLife()))
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
	s := ctx.Server.Sessions.lookup(id, secret)
	if s == nil {
		return nil
	}
	if !s.remembered() {
		s.touch()
	}
	ctx.session, ctx.sessionSecret = s, secret
	return s
}

// lookup serializes restoring a record with revocation so an in-flight restore cannot put it back.
func (ss *Sessions) lookup(id, secret string) *Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s := ss.live[id]
	if s != nil && s.expired(time.Now()) {
		delete(ss.live, id)
		s = nil
	}
	if s == nil {
		if ss.records == nil {
			return nil
		}
		rec, ok := ss.record(id)
		if !ok {
			return nil
		}
		s = sessionFromRecord(rec)
	}
	if !s.opens(secret) {
		return nil
	}
	ss.live[id] = s
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
func sessionKey(secret string) *seal.Key { return seal.NewKey(sha256.Sum256([]byte(secret))) }

// seal keeps the password under the browser's session secret.
func (ctx *Context) seal(password string) ([]byte, error) {
	if ctx.sessionSecret == "" {
		return nil, fmt.Errorf("alborz: this session carries no secret")
	}
	return sessionKey(ctx.sessionSecret).Seal([]byte(password)), nil
}

func (ctx *Context) openRemembered(sealed []byte, expires, now time.Time) (password string, ok bool) {
	if ctx.sessionSecret == "" || !now.Before(expires) {
		return "", false
	}
	raw, err := sessionKey(ctx.sessionSecret).Open(sealed)
	return string(raw), err == nil
}

// renewRemembered follows authenticated user activity, before the response is written.
// Save first: a failed write must not promise a longer cookie than the stored session.
func (ctx *Context) renewRemembered(now time.Time) error {
	return ctx.saveRemembered(now, "", nil)
}

func (ctx *Context) saveRemembered(now time.Time, account string, added []byte) error {
	s := ctx.session
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		if account != "" {
			return NewError(http.StatusUnauthorized, "session ended while signing in")
		}
		return nil
	}
	if len(s.remember) == 0 && account == "" {
		return nil
	}
	rec := s.recordLocked()
	valid := now.Before(rec.Expires)
	if !valid && account == "" {
		return NewError(http.StatusUnauthorized, "remembered sign-in expired")
	}
	kept := make(map[string][]byte, len(s.remember))
	want := make(map[string][]byte, len(s.remember)+1)
	if valid {
		maps.Copy(want, s.remember)
	}
	if account != "" {
		want[account] = added
	}
	rec.Accounts = nil
	rec.Seen, rec.Expires = now, now.Add(alborz.CredentialCookieLife())
	for account, sealed := range want {
		_, ok := ctx.openRemembered(sealed, rec.Expires, now)
		if !ok {
			continue
		}
		kept[account] = sealed
		rec.Accounts = append(rec.Accounts, alborz.RememberedAccount{Address: account, Sealed: sealed})
	}
	if len(kept) == 0 {
		if err := ctx.Server.Sessions.records.Delete(s.ID); err != nil {
			return err
		}
		s.remember = kept
		return nil
	}
	slices.SortFunc(rec.Accounts, func(a, b alborz.RememberedAccount) int { return strings.Compare(a.Address, b.Address) })
	if err := ctx.Server.Sessions.records.Save(rec); err != nil {
		return fmt.Errorf("renewing remembered sign-in: %w", err)
	}
	s.remember, s.seen, s.expires = kept, rec.Seen, rec.Expires
	cookie := ctx.cookie(sessionCookieName, s.ID+"."+ctx.sessionSecret, alborz.CredentialCookieLife())
	cookie.Expires = rec.Expires.UTC()
	ctx.SetCookie(cookie)
	return nil
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
func (ctx *Context) PutAttachment(in *File) (string, error) {
	return ctx.Session().putAttachment(in)
}

func (ctx *Context) PeekAttachment(id string) *File {
	if s := ctx.lookupSession(); s != nil {
		return s.peekAttachment(id)
	}
	return nil
}

func (ctx *Context) PopAttachment(id string) *File {
	if s := ctx.lookupSession(); s != nil {
		return s.popAttachment(id)
	}
	return nil
}

// MaxAttachmentSize bounds unsent attachments per session and per upload.
const MaxAttachmentSize = 32 << 20

var ErrAttachmentCacheSize = errors.New("attachments on session exceed maximum file size")
