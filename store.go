package alborz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"git.mehdix.org/alborz/internal/record"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// ErrNoStoreEntry is returned by Store.Get when the entry doesn't exist.
var ErrNoStoreEntry = fmt.Errorf("alborz: no such entry in store")

// Store keeps an account's settings. A write it cannot keep is
// errors.ErrUnsupported.
//
// Store must not be used from inside Login.DoIMAP.
type Store interface {
	Get(ctx context.Context, key string, out any) error
	Put(ctx context.Context, key string, v any) error
}

// Settings is a store that holds what it is given past the login, and
// can say what it holds and give it back.
type Settings interface {
	Store
	Entries(ctx context.Context) ([]string, error)
	Forget(ctx context.Context) error
	// OnServer says the account's own server holds them.
	OnServer() bool
}

// newStore picks where the account's settings live: its server's
// METADATA, else alborz's own database, else nowhere. It runs under the
// manager's lock.
func (sm *LoginManager) newStore(login *Login) (Store, error) {
	var metadata bool
	err := login.DoIMAP(func(c *imapclient.Client) error {
		metadata = c.Caps().Has(imap.CapMetadata) || c.Caps().Has(imap.CapMetadataServer)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if metadata {
		return &imapMetadataStore{login: login}, nil
	}
	if !sm.warnedNoMetadata {
		sm.warnedNoMetadata = true
		if sm.settings == nil {
			sm.logger.Print("Upstream IMAP server doesn't support the METADATA extension and alborz keeps no data, account settings cannot be saved")
		} else {
			sm.logger.Print("Upstream IMAP server doesn't support the METADATA extension, keeping account settings in alborz's own store")
		}
	}
	if sm.settings == nil {
		return noStore{}, nil
	}
	return localStore{records: sm.settings, account: login.Username()}, nil
}

// SaveRefusal is the alert for a settings write the store did not keep.
func (ctx *Context) SaveRefusal(err error) string {
	if errors.Is(err, errors.ErrUnsupported) {
		return ctx.T("form.nostorage")
	}
	return fmt.Sprintf(ctx.T("form.saverefused"), err)
}

// noStore is an account with nowhere to keep settings: every read is the
// default and every write is refused.
type noStore struct{}

func (noStore) Get(context.Context, string, any) error { return ErrNoStoreEntry }
func (noStore) Put(context.Context, string, any) error { return errors.ErrUnsupported }

type imapMetadataStore struct {
	login *Login
}

func (s *imapMetadataStore) key(key string) string {
	return vendorRoot + "/" + key
}

// storeMemo holds one request's METADATA reads, so a page asking for an
// entry twice asks the server once. It never outlives the request.
type storeMemo struct {
	mu      sync.Mutex
	entries map[memoKey][]byte
}

type memoKey struct {
	login *Login
	key   string
}

type storeMemoKey struct{}

func withStoreMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, storeMemoKey{}, &storeMemo{entries: map[memoKey][]byte{}})
}

func memoOf(ctx context.Context) *storeMemo {
	m, _ := ctx.Value(storeMemoKey{}).(*storeMemo)
	return m
}

func (m *storeMemo) forget(login *Login, key string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.entries, memoKey{login, key})
	m.mu.Unlock()
}

// fetch is the entry as the server holds it now, nil where it has none.
func (s *imapMetadataStore) fetch(ctx context.Context, key string) ([]byte, error) {
	var entries map[string]*[]byte
	err := s.login.DoIMAPContext(ctx, RoundTripTimeout, func(c *imapclient.Client) error {
		data, err := c.GetMetadata("", []string{s.key(key)}, nil).Wait()
		if err != nil {
			return err
		}
		entries = data.Entries
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("alborz: failed to fetch IMAP store entry %q: %w", key, err)
	}
	if v := entries[s.key(key)]; v != nil {
		return *v, nil
	}
	return nil, nil
}

func (s *imapMetadataStore) Get(ctx context.Context, key string, out any) error {
	m := memoOf(ctx)
	var v []byte
	var memoised bool
	if m != nil {
		m.mu.Lock()
		v, memoised = m.entries[memoKey{s.login, key}]
		m.mu.Unlock()
	}
	if !memoised {
		var err error
		if v, err = s.fetch(ctx, key); err != nil {
			return err
		}
		if m != nil {
			m.mu.Lock()
			m.entries[memoKey{s.login, key}] = v
			m.mu.Unlock()
		}
	}
	if v == nil {
		return ErrNoStoreEntry
	}
	if err := json.Unmarshal(v, out); err != nil {
		return fmt.Errorf("alborz: failed to unmarshal IMAP store entry %q: %v", key, err)
	}
	return nil
}

// Put merges into the entry as the server holds it at the moment of
// writing. METADATA has no compare-and-set, so a write in between the
// two round trips is still lost.
func (s *imapMetadataStore) Put(ctx context.Context, key string, v any) error {
	defer memoOf(ctx).forget(s.login, key)
	was, err := s.fetch(ctx, key)
	if err != nil {
		return err
	}
	b, err := record.Keep(was, v)
	if err != nil {
		return fmt.Errorf("alborz: failed to marshal IMAP store entry %q: %v", key, err)
	}
	entries := map[string]*[]byte{s.key(key): &b}
	err = s.login.DoIMAPContext(ctx, RoundTripTimeout, func(c *imapclient.Client) error {
		return c.SetMetadata("", entries).Wait()
	})
	if err != nil {
		return fmt.Errorf("alborz: failed to put IMAP store entry %q: %w", key, err)
	}
	return nil
}

// vendorRoot is where RFC 5464 puts a vendor's private entries.
const vendorRoot = "/private/vendor/alborz"

// settingKeys is every setting key a plugin stores. The entries are
// asked for by name: go-imap writes the DEPTH option after the mailbox,
// which Dovecot answers with BAD.
var settingKeys = []string{httpPasswordKey, servicesKey, profileKey}

// RegisterSetting names a key a plugin stores, so the account
// holder can see and empty what is held.
func RegisterSetting(key string) {
	settingKeys = append(settingKeys, key)
}

func (s *imapMetadataStore) Entries(ctx context.Context) ([]string, error) {
	full := make([]string, len(settingKeys))
	for i, key := range settingKeys {
		full[i] = s.key(key)
	}
	var names []string
	err := s.login.DoIMAPContext(ctx, RoundTripTimeout, func(c *imapclient.Client) error {
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

func (s *imapMetadataStore) Forget(ctx context.Context) error {
	names, err := s.Entries(ctx)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	// A nil value is how RFC 5464 spells removal.
	entries := make(map[string]*[]byte, len(names))
	for _, name := range names {
		entries[name] = nil
	}
	defer func() {
		for _, key := range settingKeys {
			memoOf(ctx).forget(s.login, key)
		}
	}()
	err = s.login.DoIMAPContext(ctx, RoundTripTimeout, func(c *imapclient.Client) error {
		return c.SetMetadata("", entries).Wait()
	})
	if err != nil {
		return fmt.Errorf("alborz: failed to remove IMAP store entries: %w", err)
	}
	return nil
}

func (s *imapMetadataStore) OnServer() bool { return true }

// localStore keeps an account's settings in alborz's own database, for
// a server with no METADATA.
type localStore struct {
	records *settingsRecords
	account string
}

func (s localStore) Get(_ context.Context, key string, out any) error {
	return s.records.Get(s.account, key, out)
}

func (s localStore) Put(_ context.Context, key string, v any) error {
	return s.records.Put(s.account, key, v)
}

func (s localStore) Entries(context.Context) ([]string, error) { return s.records.Entries(s.account) }

func (s localStore) Forget(context.Context) error { return s.records.Forget(s.account) }

func (s localStore) OnServer() bool { return false }
