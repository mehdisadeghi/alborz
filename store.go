package alborz

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// ErrNoStoreEntry is returned by Store.Get when the entry doesn't exist.
var ErrNoStoreEntry = fmt.Errorf("alborz: no such entry in store")

// Store allows storing per-user persistent data.
//
// Store shouldn't be used from inside Session.DoIMAP.
type Store interface {
	Get(key string, out interface{}) error
	Put(key string, v interface{}) error
}

// KeptStore is a store that can say what it holds and give it back.
// Both stores that outlive a session are one: the account's own server
// through METADATA, and alborz's database for a server without it. The
// account holder is entitled to see either and to empty it.
type KeptStore interface {
	Store
	// Entries names what is held, so the page can say what emptying
	// the store would remove.
	Entries() ([]string, error)
	// Forget removes every one of them.
	Forget() error
	// OnServer says the account's own server holds them, which decides
	// what the settings page has to warn about.
	OnServer() bool
}

// newStore picks the account's store: the server's METADATA when it has
// it, alborz's own database otherwise, and memory only where alborz has
// no database either. The warning is printed once, under the manager's
// lock like every other call here.
func (sm *SessionManager) newStore(session *Session) (Store, error) {
	s, err := newIMAPStore(session)
	if err == nil {
		return s, nil
	} else if err != errIMAPMetadataUnsupported {
		return nil, err
	}
	if !sm.warnedTransientStore {
		sm.logger.Print("Upstream IMAP server doesn't support the METADATA extension, keeping account settings in alborz's own store")
		sm.warnedTransientStore = true
	}
	if sm.kept == nil {
		return newMemoryStore(), nil
	}
	return newLocalStore(sm.kept, session.Username()), nil
}

type memoryStore struct {
	locker  sync.RWMutex
	entries map[string]interface{}
}

func newMemoryStore() *memoryStore {
	return &memoryStore{entries: make(map[string]interface{})}
}

func (s *memoryStore) Get(key string, out interface{}) error {
	s.locker.RLock()
	defer s.locker.RUnlock()

	v, ok := s.entries[key]
	if !ok {
		return ErrNoStoreEntry
	}

	reflect.ValueOf(out).Elem().Set(reflect.ValueOf(v).Elem())
	return nil
}

func (s *memoryStore) Put(key string, v interface{}) error {
	s.locker.Lock()
	s.entries[key] = v
	s.locker.Unlock()
	return nil
}

type imapStore struct {
	session *Session
	cache   *memoryStore
}

var errIMAPMetadataUnsupported = fmt.Errorf("alborz: IMAP server doesn't support METADATA extension")

func newIMAPStore(session *Session) (*imapStore, error) {
	err := session.DoIMAP(func(c *imapclient.Client) error {
		if caps := c.Caps(); !caps.Has(imap.CapMetadata) && !caps.Has(imap.CapMetadataServer) {
			return errIMAPMetadataUnsupported
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &imapStore{session, newMemoryStore()}, nil
}

func (s *imapStore) key(key string) string {
	return vendorRoot + "/" + key
}

func (s *imapStore) Get(key string, out interface{}) error {
	if err := s.cache.Get(key, out); err != ErrNoStoreEntry {
		return err
	}

	var entries map[string]*[]byte
	err := s.session.DoIMAP(func(c *imapclient.Client) error {
		data, err := c.GetMetadata("", []string{s.key(key)}, nil).Wait()
		if err != nil {
			return err
		}
		entries = data.Entries
		return nil
	})
	if err != nil {
		return fmt.Errorf("alborz: failed to fetch IMAP store entry %q: %w", key, err)
	}
	v, ok := entries[s.key(key)]
	if !ok || v == nil {
		return ErrNoStoreEntry
	}
	if err := json.Unmarshal(*v, out); err != nil {
		return fmt.Errorf("alborz: failed to unmarshal IMAP store entry %q: %v", key, err)
	}
	return s.cache.Put(key, out)
}

func (s *imapStore) Put(key string, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("alborz: failed to marshal IMAP store entry %q: %v", key, err)
	}
	entries := map[string]*[]byte{s.key(key): &b}
	err = s.session.DoIMAP(func(c *imapclient.Client) error {
		return c.SetMetadata("", entries).Wait()
	})
	if err != nil {
		return fmt.Errorf("alborz: failed to put IMAP store entry %q: %w", key, err)
	}

	return s.cache.Put(key, v)
}

// vendorRoot is where RFC 5464 puts a vendor's private entries. Every
// key alborz writes hangs off it, so one GETMETADATA names them all.
const vendorRoot = "/private/vendor/alborz"

// keptKeys is every key a plugin keeps in a store, named at start. The
// entries are asked for by name: the DEPTH walk of RFC 5464 needs the
// options before the mailbox, and go-imap writes them after, which
// Dovecot answers with BAD.
var keptKeys = []string{httpPasswordKey, servicesKey}

// KeepKey names a key a plugin keeps in the store, so the account
// holder can see and empty what is held.
func KeepKey(key string) {
	keptKeys = append(keptKeys, key)
}

func (s *imapStore) Entries() ([]string, error) {
	full := make([]string, len(keptKeys))
	for i, key := range keptKeys {
		full[i] = s.key(key)
	}
	var names []string
	err := s.session.DoIMAP(func(c *imapclient.Client) error {
		data, err := c.GetMetadata("", full, nil).Wait()
		if err != nil {
			return err
		}
		for name, value := range data.Entries {
			if value != nil {
				names = append(names, name)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("alborz: failed to list IMAP store entries: %w", err)
	}
	slices.Sort(names)
	return names, nil
}

func (s *imapStore) Forget() error {
	names, err := s.Entries()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	// A nil value is how RFC 5464 spells removal; there is no other way
	// to unset an entry.
	entries := make(map[string]*[]byte, len(names))
	for _, name := range names {
		entries[name] = nil
	}
	err = s.session.DoIMAP(func(c *imapclient.Client) error {
		return c.SetMetadata("", entries).Wait()
	})
	if err != nil {
		return fmt.Errorf("alborz: failed to remove IMAP store entries: %w", err)
	}
	s.cache = newMemoryStore()
	return nil
}

// localStore keeps an account's settings in alborz's own database, for
// a server with no METADATA to keep them on. The whole set is one
// record: it is a handful of small values, and reading or writing it
// whole is what makes listing and forgetting honest.
type localStore struct {
	records VisitRecords
	account string

	locker  sync.RWMutex
	entries map[string]json.RawMessage
}

func newLocalStore(records VisitRecords, account string) *localStore {
	entries, ok := records.LoadKept(account)
	if !ok {
		entries = map[string]json.RawMessage{}
	}
	return &localStore{records: records, account: account, entries: entries}
}

func (s *localStore) Get(key string, out interface{}) error {
	s.locker.RLock()
	raw, ok := s.entries[key]
	s.locker.RUnlock()
	if !ok {
		return ErrNoStoreEntry
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("alborz: failed to unmarshal store entry %q: %v", key, err)
	}
	return nil
}

func (s *localStore) Put(key string, v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("alborz: failed to marshal store entry %q: %v", key, err)
	}
	s.locker.Lock()
	defer s.locker.Unlock()
	s.entries[key] = raw
	return s.records.SaveKept(s.account, s.entries)
}

func (s *localStore) Entries() ([]string, error) {
	s.locker.RLock()
	defer s.locker.RUnlock()
	names := make([]string, 0, len(s.entries))
	for name := range s.entries {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

func (s *localStore) Forget() error {
	s.locker.Lock()
	defer s.locker.Unlock()
	s.entries = map[string]json.RawMessage{}
	return s.records.SaveKept(s.account, s.entries)
}

func (s *localStore) OnServer() bool { return false }

func (s *imapStore) OnServer() bool { return true }
