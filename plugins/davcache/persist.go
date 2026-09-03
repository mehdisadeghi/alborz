package davcache

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/fernet/fernet-go"
)

// Store keeps what the cache holds on disk between runs, so a restart
// starts warm: the first visit after a deploy pays one ctag check per
// collection instead of the whole discovery and every query. Files are
// sealed under the login key, since a calendar is personal data and
// the key is what the deployment already guards; a rotated key makes
// the next start cold, nothing worse.
type Store struct {
	dir string
	key *fernet.Key
}

func NewStore(dir string, key *fernet.Key) *Store {
	return &Store{dir: dir, key: key}
}

type storedUser struct {
	Username   string
	LastActive time.Time
	Ctags      map[string]string
	Entries    []storedEntry
}

type storedEntry struct {
	Status  int
	Header  http.Header
	Body    []byte
	Fetched time.Time
	LastUse time.Time
	Method  string
	URL     string
	Depth   string
	ReqBody []byte
}

// path names the user's file without naming the user.
func (s *Store) path(username string) string {
	sum := sha256.Sum256([]byte(username))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:16]))
}

func (s *Store) save(username string, u *user) error {
	u.mu.Lock()
	su := storedUser{Username: username, LastActive: u.lastActive, Ctags: u.ctags}
	for _, e := range u.entries {
		su.Entries = append(su.Entries, storedEntry{
			Status: e.status, Header: e.header, Body: e.body,
			Fetched: e.fetched, LastUse: e.lastUse,
			Method: e.method, URL: e.url.String(), Depth: e.depth, ReqBody: e.reqBody,
		})
	}
	u.mu.Unlock()

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&su); err != nil {
		return err
	}
	sealed, err := fernet.EncryptAndSign(buf.Bytes(), s.key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	// Written beside and renamed over, so a crash mid-write leaves the
	// last good file rather than half of a new one.
	tmp := s.path(username) + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(username))
}

func (s *Store) remove(username string) error {
	err := os.Remove(s.path(username))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// load reads every user file the key opens. One it cannot open was
// sealed under another key and is dropped: it would never open again.
func (s *Store) load(poll time.Duration) (map[string]*user, error) {
	names, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return map[string]*user{}, nil
	}
	if err != nil {
		return nil, err
	}
	users := map[string]*user{}
	for _, name := range names {
		if name.IsDir() || filepath.Ext(name.Name()) == ".tmp" {
			continue
		}
		path := filepath.Join(s.dir, name.Name())
		sealed, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		raw := fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{s.key})
		if raw == nil {
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			continue
		}
		var su storedUser
		if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&su); err != nil {
			return nil, err
		}
		u := newUser(poll)
		u.lastActive = su.LastActive
		if su.Ctags != nil {
			u.ctags = su.Ctags
		}
		for _, se := range su.Entries {
			target, err := url.Parse(se.URL)
			if err != nil {
				return nil, err
			}
			e := &entry{
				status: se.Status, header: se.Header, body: se.Body,
				fetched: se.Fetched, lastUse: se.LastUse,
				method: se.Method, url: target, depth: se.Depth, reqBody: se.ReqBody,
			}
			u.entries[cacheKey(e.method, e.url.Path, e.depth, e.reqBody)] = e
		}
		users[su.Username] = u
	}
	return users, nil
}
