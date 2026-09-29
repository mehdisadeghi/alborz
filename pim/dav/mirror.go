package dav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"github.com/emersion/go-webdav"
	"github.com/fernet/fernet-go"
)

const (
	// syncWait is how long a read waits for a first copy before asking the server, well inside requestTimeout.
	syncWait = 4 * time.Second
	// syncBudget bounds one collection's sync, every round trip of it.
	syncBudget = 30 * time.Second
	// tick is the round's pace; what would go stale before the next one is synced now.
	tick        = 10 * time.Second
	syncWorkers = 4
	lockTimeout = 5 * time.Second
)

// Mirror keeps a copy of each remote collection an account reads, in step with its server (ADR 53).
// A nil Mirror keeps nothing: every read goes to the server.
type Mirror struct {
	db   kv.DB
	key  *fernet.Key
	wire wire
	poll time.Duration

	// saving orders a sync's result against own writes and deletes made while it ran.
	saving  sync.Mutex
	mu      sync.Mutex
	readers map[string]*reader
	closed  bool
	work    sync.WaitGroup
}

type reader struct {
	// out is the account's way to its servers as its last request had it; background syncs use it.
	out   http.RoundTripper
	colls map[string]*collection
}

type collection struct {
	syncing chan struct{}
	failed  time.Time
	// writes counts own writes; a sync that began before one read a stale collection.
	writes uint64
	// read says it was read since start, so the loop keeps it in step.
	read bool
}

func (r *reader) coll(p string) *collection {
	c := r.colls[p]
	if c == nil {
		c = &collection{}
		r.colls[p] = c
	}
	return c
}

type mirrored struct {
	URL string
	// NoSync says the server has no sync token; the copy is compared by etag, skipped while CTag stands.
	Token, CTag string
	NoSync      bool
	Synced      time.Time
}

func (h mirrored) target() *url.URL {
	u, err := url.Parse(h.URL)
	if err != nil {
		panic(err)
	}
	return u
}

type object struct {
	Name, ETag string
	Data       []byte
}

// Every key part is hashed so the file names nobody.
const owner = "dav.mirror"

func hashed(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

func heldKey(u, coll string) string    { return owner + "/held/" + u + "/" + hashed(coll) }
func objectsKey(u, coll string) string { return owner + "/objects/" + u + "/" + hashed(coll) + "/" }

// OpenMirror opens the kind's copies in dir; round keeps them in step.
func OpenMirror(dir string, kind Kind, key *fernet.Key) (*Mirror, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, kind.Name+".db")
	db, err := bolt.Open(file, lockTimeout)
	if errors.Is(err, kv.ErrLocked) {
		return nil, fmt.Errorf("%s is locked: another alborz is running on this cache directory; "+
			"stop it, or give this one another $XDG_CACHE_HOME", file)
	}
	if err != nil {
		return nil, err
	}
	m := &Mirror{db: db, key: key, wire: wireFor(kind.Holds), poll: kind.Poll,
		readers: map[string]*reader{}}
	if err := alborz.CheckKey(db, file, key, owner+"/"); err != nil {
		db.Close()
		return nil, err
	}
	return m, nil
}

func (m *Mirror) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.work.Wait()
	return m.db.Close()
}

func (m *Mirror) put(tx kv.Tx, k string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	sealed, err := fernet.EncryptAndSign(raw, m.key)
	if err != nil {
		return err
	}
	return tx.Put(k, sealed)
}

func (m *Mirror) open(sealed []byte, v any) error {
	return json.Unmarshal(fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{m.key}), v)
}

func scan[T any](m *Mirror, prefix string) ([]T, error) {
	var out []T
	err := m.db.View(func(tx kv.Tx) error {
		return tx.Scan(prefix, func(_ string, sealed []byte) error {
			var v T
			err := m.open(sealed, &v)
			out = append(out, v)
			return err
		})
	})
	return out, err
}

func (m *Mirror) held(u, coll string) (mirrored, bool, error) {
	var h mirrored
	var ok bool
	err := m.db.View(func(tx kv.Tx) error {
		sealed := tx.Get(heldKey(u, coll))
		if sealed == nil {
			return nil
		}
		ok = true
		return m.open(sealed, &h)
	})
	return h, ok, err
}

func (m *Mirror) heldOf(u string) ([]mirrored, error) {
	return scan[mirrored](m, owner+"/held/"+u+"/")
}

func (m *Mirror) objects(u, coll string) ([]object, error) {
	out, err := scan[object](m, objectsKey(u, coll))
	slices.SortFunc(out, func(a, b object) int { return strings.Compare(a.Name, b.Name) })
	return out, err
}

func (m *Mirror) etags(u, coll string) (map[string]string, error) {
	objects, err := m.objects(u, coll)
	tags := make(map[string]string, len(objects))
	for _, o := range objects {
		tags[o.Name] = o.ETag
	}
	return tags, err
}

// The caller holds mu.
func (m *Mirror) reader(u string) *reader {
	r, ok := m.readers[u]
	if !ok {
		r = &reader{colls: map[string]*collection{}}
		m.readers[u] = r
	}
	return r
}

func (m *Mirror) note(u string, out http.RoundTripper) {
	m.mu.Lock()
	m.reader(u).out = out
	m.mu.Unlock()
}

// Principal is whom a copy is kept for: the account, and the name its servers know it by.
// A server may answer one path differently per name, so a copy is never served to another (ADR 53).
type Principal struct {
	Account, Name string
}

func (p Principal) key() string { return hashed(p.Account) + "/" + hashed(p.Name) }

// Transport answers reads of held collections and passes everything else to next.
func (m *Mirror) Transport(who Principal, next http.RoundTripper) http.RoundTripper {
	if m == nil {
		return next
	}
	return tripper{m: m, user: who.key(), next: next}
}

type tripper struct {
	m    *Mirror
	user string
	next http.RoundTripper
}

func (t tripper) RoundTrip(req *http.Request) (*http.Response, error) {
	t.m.note(t.user, t.next)
	switch req.Method {
	case "REPORT", http.MethodGet:
		return t.read(req)
	case http.MethodPut, http.MethodDelete, "MKCOL", "MKCALENDAR", "PROPPATCH":
		return t.write(req)
	}
	return t.next.RoundTrip(req)
}

// collectionOf is the collection a request reads: a query names it, an object read its parent.
func collectionOf(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return path.Dir(p) + "/"
}

func (t tripper) read(req *http.Request) (*http.Response, error) {
	req, body, err := buffered(req)
	if err != nil {
		return nil, err
	}
	if req.Method == "REPORT" && !t.m.wire.answers(body) {
		return t.next.RoundTrip(req)
	}
	target := *req.URL
	target.Path, target.RawPath, target.RawQuery = collectionOf(req.URL.Path), "", ""
	hit, err := t.m.ready(req.Context(), t.user, &target)
	if err != nil {
		return nil, err
	}
	alborz.CacheTiming(req.Context(), "dav_cache", hit)
	if !hit {
		return t.next.RoundTrip(req)
	}
	objects, err := t.m.objects(t.user, target.Path)
	if err != nil {
		return nil, err
	}
	return respond(t.m.wire.handler(target.Path, objects), req), nil
}

// ready says whether the copy can answer: a held one can, however old; one not held is synced first.
func (m *Mirror) ready(ctx context.Context, u string, target *url.URL) (bool, error) {
	h, ok, err := m.held(u, target.Path)
	if err != nil {
		return false, err
	}
	m.mu.Lock()
	c := m.reader(u).coll(target.Path)
	c.read = true
	failed := time.Since(c.failed) < m.poll
	m.mu.Unlock()
	if ok {
		if time.Since(h.Synced) > m.poll {
			m.start(alborz.Background(context.Background()), u, target)
		}
		return true, nil
	}
	if failed {
		return false, nil
	}
	done, _ := m.start(context.WithoutCancel(ctx), u, target)
	select {
	case <-done:
	case <-time.After(syncWait):
	case <-ctx.Done():
	}
	_, ok, err = m.held(u, target.Path)
	return ok, err
}

// start syncs the collection unless a sync is under way, in which case began is false.
func (m *Mirror) start(ctx context.Context, u string, target *url.URL) (done <-chan struct{}, began bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.reader(u)
	c := r.coll(target.Path)
	if c.syncing != nil {
		return c.syncing, false
	}
	ch := make(chan struct{})
	if m.closed || r.out == nil {
		close(ch)
		return ch, true
	}
	c.syncing = ch
	out, written := r.out, c.writes
	m.work.Add(1)
	go func() {
		defer m.work.Done()
		ctx, cancel := context.WithTimeout(ctx, syncBudget)
		err := m.sync(ctx, r, u, target, out, written)
		cancel()
		m.mu.Lock()
		c.syncing, c.failed = nil, time.Time{}
		if err != nil {
			c.failed = time.Now()
		}
		m.mu.Unlock()
		close(ch)
	}()
	return ch, true
}

func (m *Mirror) sync(ctx context.Context, r *reader, u string, target *url.URL, out http.RoundTripper, written uint64) error {
	client := &http.Client{Transport: out, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	h, _, err := m.held(u, target.Path)
	if err != nil {
		return err
	}
	h.URL = target.String()
	d, err := m.wire.pull(ctx, client, target, h, func() (map[string]string, error) { return m.etags(u, target.Path) })
	if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
		return errors.Join(err, m.drop(u, target.Path))
	}
	if err != nil {
		return err
	}
	m.saving.Lock()
	defer m.saving.Unlock()
	m.mu.Lock()
	current := m.readers[u] == r && r.coll(target.Path).writes == written
	m.mu.Unlock()
	// What was read may predate an own write; the next sync brings both.
	if !current {
		return nil
	}
	h.Token, h.CTag, h.NoSync, h.Synced = d.token, d.ctag, d.noSync, time.Now()
	return m.db.Update(func(tx kv.Tx) error {
		prefix := objectsKey(u, target.Path)
		if d.whole {
			if err := kv.DeleteUnder(tx, prefix, nil); err != nil {
				return err
			}
		}
		for _, name := range d.gone {
			if err := tx.Delete(prefix + hashed(name)); err != nil {
				return err
			}
		}
		for _, o := range d.put {
			if err := m.put(tx, prefix+hashed(o.Name), o); err != nil {
				return err
			}
		}
		return m.put(tx, heldKey(u, target.Path), h)
	})
}

func (t tripper) write(req *http.Request) (*http.Response, error) {
	req, body, err := buffered(req)
	if err != nil {
		return nil, err
	}
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if err := t.m.wrote(t.user, req, body, resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

// wrote applies an own write to the copy (ADR 20); an ETag means stored octet for octet
// (RFC 4791 5.3.4, RFC 6352 6.3.2.3). Without one, or refused, the next read syncs.
func (m *Mirror) wrote(u string, req *http.Request, body []byte, resp *http.Response) error {
	p := req.URL.Path
	if strings.HasSuffix(p, "/") || req.Method != http.MethodPut && req.Method != http.MethodDelete {
		if resp.StatusCode >= 300 {
			return nil
		}
		return m.drop(u, collectionOf(p))
	}
	coll, name := path.Split(p)
	m.saving.Lock()
	defer m.saving.Unlock()
	m.mu.Lock()
	m.reader(u).coll(coll).writes++
	m.mu.Unlock()
	h, ok, err := m.held(u, coll)
	if err != nil || !ok {
		return err
	}
	etag := resp.Header.Get("ETag")
	return m.db.Update(func(tx kv.Tx) error {
		k := objectsKey(u, coll) + hashed(name)
		var err error
		switch {
		case resp.StatusCode >= 300:
			h.Synced = time.Time{}
		case req.Method == http.MethodDelete:
			err = tx.Delete(k)
		default:
			if etag == "" {
				h.Synced = time.Time{}
			}
			err = m.put(tx, k, object{Name: name, ETag: etag, Data: body})
		}
		if err != nil {
			return err
		}
		return m.put(tx, heldKey(u, coll), h)
	})
}

func (m *Mirror) drop(u, coll string) error {
	return m.db.Update(func(tx kv.Tx) error {
		if err := tx.Delete(heldKey(u, coll)); err != nil {
			return err
		}
		return kv.DeleteUnder(tx, objectsKey(u, coll), nil)
	})
}

// Forget stops keeping a signed-out account in step under any name; its sealed copies stay for its return.
func (m *Mirror) Forget(username string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	for u := range m.readers {
		if strings.HasPrefix(u, hashed(username)+"/") {
			delete(m.readers, u)
		}
	}
	m.mu.Unlock()
}

// Refresh syncs every held collection now; a sync under way is followed by a new one, as it may predate the change.
func (m *Mirror) Refresh(ctx context.Context, who Principal, next http.RoundTripper) error {
	if m == nil {
		return nil
	}
	u := who.key()
	m.note(u, next)
	all, err := m.heldOf(u)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, syncBudget)
	defer cancel()
	for _, h := range all {
		for range 2 {
			done, began := m.start(ctx, u, h.target())
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
			if began {
				break
			}
		}
	}
	return nil
}

// round syncs the collections read since start ahead of staleness.
func (m *Mirror) round(ctx context.Context, now time.Time) {
	m.mu.Lock()
	var users []string
	for u, r := range m.readers {
		if r.out != nil {
			users = append(users, u)
		}
	}
	m.mu.Unlock()
	parallel(users, func(u string) {
		all, err := m.heldOf(u)
		if err != nil {
			return
		}
		var due []mirrored
		for _, h := range all {
			coll := h.target().Path
			m.mu.Lock()
			c := m.reader(u).coll(coll)
			read, failed := c.read, c.failed
			m.mu.Unlock()
			if read && now.Sub(h.Synced) > m.poll-tick && now.Sub(failed) > m.poll-tick {
				due = append(due, h)
			}
		}
		parallel(due, func(h mirrored) {
			done, _ := m.start(alborz.Background(ctx), u, h.target())
			<-done
		})
	})
}

func parallel[T any](values []T, f func(T)) {
	jobs := make(chan T)
	var wg sync.WaitGroup
	for range min(syncWorkers, len(values)) {
		wg.Go(func() {
			for v := range jobs {
				f(v)
			}
		})
	}
	for _, v := range values {
		jobs <- v
	}
	close(jobs)
	wg.Wait()
}
