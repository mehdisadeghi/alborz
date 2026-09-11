package alborz

import (
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

	"github.com/fernet/fernet-go"
	"github.com/google/uuid"
)

// A Visit is one browser's stay: the accounts it has signed into and
// what the reader is owed. It exists because a notice and an unsent
// upload belong to the person at the screen and to no account, and
// filing them under one account is how a notice raised on a page
// scoped to it turns up later on something else.
//
// The session holds what IMAP forces, a credential and a connection.
// Everything the reader would recognise as theirs lives here.
type Visit struct {
	ID      string
	Created time.Time

	mu          sync.Mutex
	seen        time.Time
	notice      *Notice
	attachments map[string]*Attachment
	// accounts is the bag: every account this browser has signed into,
	// in one order wherever they are listed.
	accounts []*Session
	// remember holds each account's password sealed under the secret
	// the browser carries, for a visit the reader asked to keep.
	remember map[string][]byte
	// read is what the pages are read by, and anchor the account it is
	// kept on. A nil read means the browser has chosen nothing yet and
	// will take what the first account brings.
	read   *Reading
	anchor string
}

func (v *Visit) reading() Reading {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.read == nil {
		return Reading{}
	}
	return *v.read
}

func (v *Visit) hasReading() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.read != nil
}

func (v *Visit) setReading(r Reading) {
	v.mu.Lock()
	v.read = &r
	v.mu.Unlock()
}

// Anchor is the account this browser's reading settings are kept on,
// empty when they are kept nowhere.
func (v *Visit) Anchor() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.anchor
}

func (v *Visit) setAnchor(account string) {
	v.mu.Lock()
	v.anchor = account
	v.mu.Unlock()
}

// life is how long the visit outlives its last request: as long as a
// session when nobody asked to be remembered, and as long as the
// credentials are worth keeping when they did.
func (v *Visit) life() time.Duration {
	if v.remembered() {
		return credentialCookieLife
	}
	return visitDuration
}

// remembered reports whether the reader asked this browser to stay
// signed in.
func (v *Visit) remembered() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.remember) > 0
}

// Remember keeps the account's password for this visit, sealed under
// the browser's own secret, so a restart costs a reconnection rather
// than a login. Forgetting it is signing out.
func (v *Visit) Remember(address string, sealed []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.remember == nil {
		v.remember = map[string][]byte{}
	}
	v.remember[address] = sealed
}

func (v *Visit) forget(address string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.remember, address)
}

// record is the visit as it is written down: no notice, no uploads,
// nothing that would mean anything after a restart.
func (v *Visit) record() *VisitRecord {
	v.mu.Lock()
	defer v.mu.Unlock()
	rec := &VisitRecord{ID: v.ID, Created: v.Created, Seen: v.seen,
		Reading: v.read, Anchor: v.anchor}
	for address, sealed := range v.remember {
		rec.Accounts = append(rec.Accounts, RememberedAccount{Address: address, Sealed: sealed})
	}
	slices.SortFunc(rec.Accounts, func(a, b RememberedAccount) int {
		return strings.Compare(a.Address, b.Address)
	})
	return rec
}

// visitFromRecord rebuilds a remembered visit, with no accounts open
// yet: the sessions are made when the first request needs them.
func visitFromRecord(rec *VisitRecord) *Visit {
	v := &Visit{
		ID:          rec.ID,
		Created:     rec.Created,
		seen:        time.Now(),
		attachments: map[string]*Attachment{},
		remember:    map[string][]byte{},
	}
	for _, a := range rec.Accounts {
		v.remember[a.Address] = a.Sealed
	}
	v.read, v.anchor = rec.Reading, rec.Anchor
	return v
}

// Accounts is the bag, with the accounts whose session has ended left
// out and named, so the page can say who was signed out rather than
// pretending they are still here.
func (v *Visit) Accounts() (live []*Session, lost []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	kept := v.accounts[:0]
	for _, s := range v.accounts {
		if s.alive() {
			kept = append(kept, s)
			continue
		}
		lost = append(lost, s.Username())
	}
	v.accounts = kept
	return slices.Clone(v.accounts), lost
}

// Add puts an account in the bag, replacing an earlier session for the
// same address, and keeps the bag in one order.
func (v *Visit) Add(s *Session) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i, other := range v.accounts {
		if other.username == s.username {
			other.Close()
			v.accounts[i] = s
			byName(v.accounts)
			return
		}
	}
	v.accounts = append(v.accounts, s)
	byName(v.accounts)
}

// Remove takes an account out of the bag and closes it.
func (v *Visit) Remove(username string) *Session {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i, s := range v.accounts {
		if s.username == username {
			v.accounts = append(v.accounts[:i], v.accounts[i+1:]...)
			return s
		}
	}
	return nil
}

// visitCookieName carries the id and nothing else: what the visit holds
// is the server's business, and the browser only has to say which one
// it is.
const visitCookieName = "alborz_visit"

// VisitRecord is what a visit is worth keeping across a restart: who
// it has signed into, and the passwords to sign in again, each sealed
// under a key only the browser's cookie carries.
type VisitRecord struct {
	ID       string
	Created  time.Time
	Seen     time.Time
	Accounts []RememberedAccount
	Reading  *Reading
	Anchor   string
}

// RememberedAccount is one account of a remembered visit.
type RememberedAccount struct {
	Address string
	Sealed  []byte
}

// VisitRecords is where remembered visits live. Nothing is written
// until a reader asks to be remembered, and a deployment without a
// store simply cannot offer it. A bbolt file implements this today; a
// Redis or SQL one would implement the same four methods.
type VisitRecords interface {
	Load(id string) (*VisitRecord, bool)
	Save(rec *VisitRecord) error
	Delete(id string) error
	Sweep(before time.Time) error
	// LoadReading and SaveReading keep what a person reads by on the
	// account they anchored it to, which is the only identity alborz
	// has to hang it on.
	LoadReading(account string) (*Reading, bool)
	SaveReading(account string, r *Reading) error
	DeleteReading(account string) error
	// LoadKept and SaveKept hold what an account's own server will
	// not: without METADATA there is nowhere on it to write, and the
	// settings would otherwise last only as long as the process.
	LoadKept(account string) (map[string]json.RawMessage, bool)
	SaveKept(account string, entries map[string]json.RawMessage) error
}

// Visits are the browsers signed in. The live ones are held here, and
// the remembered ones are also written to the records, so a restart
// costs a reconnection rather than a login.
type Visits struct {
	mu      sync.Mutex
	live    map[string]*Visit
	records VisitRecords
}

func newVisits() *Visits {
	return &Visits{live: map[string]*Visit{}}
}

// Records makes remembering possible. Without one, a visit lasts as
// long as the process.
func (vs *Visits) Records(r VisitRecords) { vs.records = r }

// Remembers reports whether this deployment can remember a visit.
func (vs *Visits) Remembers() bool { return vs.records != nil }

// Keeper is where alborz writes for an account whose server will not,
// nil when this deployment has nowhere of its own to write.
func (vs *Visits) Keeper() VisitRecords { return vs.records }

func (vs *Visits) Get(id string) *Visit {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	v := vs.live[id]
	if v == nil {
		return nil
	}
	if time.Since(v.lastSeen()) > v.life() {
		delete(vs.live, id)
		return nil
	}
	return v
}

func (vs *Visits) Put(v *Visit) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.live[v.ID] = v
}

func (vs *Visits) Delete(id string) {
	vs.mu.Lock()
	v := vs.live[id]
	delete(vs.live, id)
	vs.mu.Unlock()
	if v != nil {
		v.close()
	}
	if vs.records != nil {
		vs.records.Delete(id)
	}
}

// Record is the remembered visit of that id, when there is one.
func (vs *Visits) Record(id string) (*VisitRecord, bool) {
	if vs.records == nil {
		return nil, false
	}
	return vs.records.Load(id)
}

// Save writes what a remembered visit is made of. A visit nobody asked
// to remember is never written.
func (vs *Visits) Save(v *Visit) error {
	if vs.records == nil || !v.remembered() {
		return nil
	}
	return vs.records.Save(v.record())
}

// LoadReading and SaveReading are what an account keeps for whoever
// anchored their reading settings to it.
func (vs *Visits) LoadReading(account string) (*Reading, bool) {
	if vs.records == nil {
		return nil, false
	}
	return vs.records.LoadReading(account)
}

func (vs *Visits) SaveReading(account string, r Reading) error {
	if vs.records == nil {
		return nil
	}
	return vs.records.SaveReading(account, &r)
}

// Sweep drops the records nobody has come back to.
func (vs *Visits) Sweep(before time.Time) {
	if vs.records != nil {
		vs.records.Sweep(before)
	}
}

// visitDuration is how long a visit outlives its last request. It
// matches the session's, so nothing changes hands while a reader is
// signed in; a persisted visit will outlive both.
const visitDuration = sessionDuration

// newVisit makes a visit and the secret its browser keeps.
func newVisit() (*Visit, string) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		panic(err) // A machine that cannot make a random id cannot serve mail.
	}
	now := time.Now()
	v := &Visit{
		ID:          base64.RawURLEncoding.EncodeToString(b[:32]),
		Created:     now,
		seen:        now,
		attachments: map[string]*Attachment{},
	}
	return v, base64.RawURLEncoding.EncodeToString(b[32:])
}

func (v *Visit) lastSeen() time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.seen
}

func (v *Visit) touch() {
	v.mu.Lock()
	v.seen = time.Now()
	v.mu.Unlock()
}

// Notify records what the next page tells the reader.
func (v *Visit) Notify(n Notice) {
	v.mu.Lock()
	v.notice = &n
	v.mu.Unlock()
}

// PopNotice takes what the reader is owed, once.
func (v *Visit) PopNotice() *Notice {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := v.notice
	v.notice = nil
	return n
}

// PutAttachment keeps a file the compose form has taken but not sent.
func (v *Visit) PutAttachment(in *multipart.FileHeader, form *multipart.Form) (string, error) {
	id := uuid.New()
	v.mu.Lock()
	defer v.mu.Unlock()

	var size int64
	for _, a := range v.attachments {
		size += a.File.Size
	}
	if size+in.Size > MaxAttachmentSize {
		return "", ErrAttachmentCacheSize
	}
	v.attachments[id.String()] = &Attachment{File: in, Form: form}
	return id.String(), nil
}

// PopAttachment takes one back, once.
func (v *Visit) PopAttachment(id string) *Attachment {
	v.mu.Lock()
	defer v.mu.Unlock()
	a, ok := v.attachments[id]
	if !ok {
		return nil
	}
	delete(v.attachments, id)
	return a
}

// close drops what the visit was holding for a reader who is leaving.
func (v *Visit) close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, a := range v.attachments {
		if a.Form != nil {
			a.Form.RemoveAll()
		}
	}
	v.attachments = map[string]*Attachment{}
}

// Visit is this browser's stay, made on first use. A request that
// stores nothing never makes one, so a crawler and the login page cost
// nothing.
func (ctx *Context) Visit() *Visit {
	if v := ctx.lookupVisit(); v != nil {
		return v
	}
	v, secret := newVisit()
	ctx.Server.Visits.Put(v)
	ctx.SetCookie(ctx.cookie(visitCookieName, v.ID+"."+secret, credentialCookieLife))
	ctx.visit, ctx.visitSecret = v, secret
	return v
}

// lookupVisit finds this browser's stay without making one. The cookie
// carries the id and a secret: the id says which visit, and the secret
// is the only key to the passwords a remembered visit holds, so the
// store on its own opens nothing.
func (ctx *Context) lookupVisit() *Visit {
	if ctx.visit != nil {
		return ctx.visit
	}
	value, ok := ctx.cookieValue(visitCookieName, func(string) bool { return true })
	if !ok {
		return nil
	}
	id, secret, _ := strings.Cut(value, ".")
	v := ctx.Server.Visits.Get(id)
	if v == nil {
		rec, found := ctx.Server.Visits.Record(id)
		if !found {
			return nil
		}
		v = visitFromRecord(rec)
		ctx.Server.Visits.Put(v)
	}
	v.touch()
	ctx.visit, ctx.visitSecret = v, secret
	return v
}

// visitKey is the key the browser's secret stands for. The secret
// never leaves the cookie, so a stolen store decrypts nothing.
func visitKey(secret string) *fernet.Key {
	sum := sha256.Sum256([]byte(secret))
	var key fernet.Key
	copy(key[:], sum[:])
	return &key
}

// seal locks a password to this browser, and unseal opens it again.
func (ctx *Context) seal(password string) ([]byte, error) {
	if ctx.visitSecret == "" {
		return nil, fmt.Errorf("alborz: this visit carries no secret")
	}
	return fernet.EncryptAndSign([]byte(password), visitKey(ctx.visitSecret))
}

func (ctx *Context) unseal(sealed []byte) (string, bool) {
	if ctx.visitSecret == "" {
		return "", false
	}
	raw := fernet.VerifyAndDecrypt(sealed, credentialCookieLife, []*fernet.Key{visitKey(ctx.visitSecret)})
	if raw == nil {
		return "", false
	}
	return string(raw), true
}

// Notify records what the next page tells the reader, and PutNotice
// says a request did what it was asked.
func (ctx *Context) Notify(n Notice) { ctx.Visit().Notify(n) }

func (ctx *Context) PutNotice(text string) {
	ctx.Notify(Notice{Kind: NoticeDone, Text: text})
}

func (ctx *Context) PopNotice() *Notice {
	if v := ctx.lookupVisit(); v != nil {
		return v.PopNotice()
	}
	return nil
}

// PutAttachment and PopAttachment hold what the compose form has taken
// and not yet sent.
func (ctx *Context) PutAttachment(in *multipart.FileHeader, form *multipart.Form) (string, error) {
	return ctx.Visit().PutAttachment(in, form)
}

func (ctx *Context) PopAttachment(id string) *Attachment {
	if v := ctx.lookupVisit(); v != nil {
		return v.PopAttachment(id)
	}
	return nil
}
