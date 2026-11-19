package dav_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	"github.com/emersion/go-webdav"
)

type discoveryTransport func(*http.Request) (*http.Response, error)

func (f discoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPeersAnnouncedAndRead(t *testing.T) {
	imap := IMAP(t, Metadata())
	a, _ := SignedIn(t, User, NoCache(), WithIMAP(imap))
	login := a.Server.Logins()[0]
	options := *a.Server.Options
	options.DataDir, options.CacheDir, options.PublicURL = t.TempDir(), t.TempDir(), "https://alborz.example.test"
	srv, err := alborz.New(a.Server.Logger(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	// alias.example.test is this alborz by another name.
	fake := discoveryTransport(func(r *http.Request) (*http.Response, error) {
		header := http.Header{}
		if r.URL.Host == "alias.example.test" {
			header.Set(alborz.InstanceHeader, srv.Instance)
		}
		return &http.Response{StatusCode: http.StatusNotFound, Header: header, Body: http.NoBody, Request: r}, nil
	})
	srv.Outgoing.Trusted, srv.Outgoing.Guarded = fake, fake
	db, err := bolt.Open(filepath.Join(t.TempDir(), "collections.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db, srv.LoginKey, srv.Keys)
	p, err := dav.NewProvider(srv, st, dav.Kind{
		Name: "caldav", Label: "calendar",
		Schemes:  [2]string{"caldavs", "caldav+insecure"},
		Discover: func(context.Context, string) (string, error) { return "", errors.New("no SRV records") },
		Own:      func(dav.Services) string { return "" },
		FindHome: func(_ context.Context, _ *http.Client, endpoint string) (string, error) { return endpoint, nil },
		List: func(context.Context, *http.Client, *url.URL, []dav.Home) ([]dav.Collection, []error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	here := "https://alborz.example.test/alborz/dav/"
	other := "https://other.example.test/alborz/dav/"
	if err := login.Store().Put(ctx, dav.PeersKey, []string{other, "https://alias.example.test/alborz/dav/", "http://plain.example.test/alborz/dav/"}); err != nil {
		t.Fatal(err)
	}
	listed := func() []string {
		var peers []string
		if err := login.Store().Get(ctx, dav.PeersKey, &peers); err != nil {
			t.Fatal(err)
		}
		return peers
	}
	collections := func() {
		if _, err := p.Collections(ctx, login); err != nil {
			t.Fatal(err)
		}
	}

	c, err := st.Create(store.Collection{Owner: User, Kind: store.Calendar, Name: "Here"})
	if err != nil {
		t.Fatal(err)
	}
	collections()
	if got := listed(); len(got) != 4 || got[0] != here {
		t.Fatalf("keeping a calendar here listed %q", got)
	}
	var peers []string
	for _, s := range p.Sources(ctx, login) {
		if s.ID == dav.SourcePeer {
			peers = append(peers, s.URL.String())
		}
	}
	if len(peers) != 1 || peers[0] != other {
		t.Errorf("peer sources %q, want only %s", peers, other)
	}

	if err := st.Delete(c.Ref()); err != nil {
		t.Fatal(err)
	}
	collections()
	if got := listed(); len(got) != 3 || slices.Contains(got, here) {
		t.Errorf("keeping nothing here left %q", got)
	}
}

func TestFirstDAVReadWaitsForMailHostDiscovery(t *testing.T) {
	for _, upstream := range []string{Domain + "=", ""} {
		t.Run("upstream="+upstream, func(t *testing.T) { firstDAVRead(t, upstream) })
	}
}

func firstDAVRead(t *testing.T, upstream string) {
	a, _ := SignedIn(t, User, NoCache())
	login := a.Server.Logins()[0]
	options := *a.Server.Options
	options.DataDir, options.CacheDir = t.TempDir(), t.TempDir()
	options.Domains, options.Provider = nil, nil
	if err := options.ParseUpstreams([]string{upstream + "imap+insecure://mail.example.test:143"}); err != nil {
		t.Fatal(err)
	}
	srv, err := alborz.New(a.Server.Logger(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	srv.Outgoing.Trusted = discoveryTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodOptions {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Dav": {"calendar-access"}}, Body: io.NopCloser(strings.NewReader("remote body")), Request: r}, nil
	})
	db, err := bolt.Open(filepath.Join(t.TempDir(), "collections.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p, err := dav.NewProvider(srv, store.New(db, srv.LoginKey, srv.Keys), dav.Kind{
		Name: "caldav", Label: "calendar",
		Schemes:  [2]string{"caldavs", "caldav+insecure"},
		Discover: func(context.Context, string) (string, error) { return "", errors.New("no SRV records") },
		Own:      func(dav.Services) string { return "" },
		FindHome: func(_ context.Context, _ *http.Client, endpoint string) (string, error) { return endpoint, nil },
		List: func(_ context.Context, _ *http.Client, _ *url.URL, homes []dav.Home) ([]dav.Collection, []error) {
			for _, home := range homes {
				if home.Source.ID == dav.SourceHost {
					return []dav.Collection{{Name: "Remote calendar", Path: "/@" + home.Source.URL.Host + "/remote/"}}, nil
				}
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Collections(ctx, login); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled discovery wait returned %v", err)
	}
	if _, err := p.HTTPClient(ctx, login).Get("http://alborz.invalid/remote/"); !errors.Is(err, context.Canceled) {
		t.Errorf("a client built during a canceled discovery wait returned %v", err)
	}
	type result struct {
		client *http.Client
		rows   []dav.Collection
		err    error
	}
	done := make(chan result, 1)
	go func() {
		client, rows, err := dav.Opened(context.Background(), p, login, func(ctx context.Context, _ *alborz.Login) (*http.Client, error) { return p.HTTPClient(ctx, login), nil })
		done <- result{client, rows, err}
	}()
	select {
	case got := <-done:
		t.Fatalf("collection list answered before discovery finished: %d rows, %v", len(got.rows), got.err)
	case <-time.After(25 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	select {
	case got := <-done:
		if got.err != nil || len(got.rows) != 1 || got.rows[0].Name != "Remote calendar" {
			t.Fatalf("first collection list lost the discovered calendar: %+v, %v", got.rows, got.err)
		}
		response, err := got.client.Get(dav.Base().ResolveReference(&url.URL{Path: got.rows[0].Path + "entry.ics"}).String())
		if err != nil {
			t.Fatalf("first client cannot reach the collection it discovered: %v", err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || string(body) != "remote body" {
			t.Fatalf("first read: %q, %v", body, err)
		}
	case <-time.After(time.Second):
		t.Fatal("discovery completion did not release the first collection list")
	}
}

// A calendar deleted through alborz and restored on its server, as from Nextcloud's trash bin, is listed
// again with its objects behind no page, and so is one another client makes; a server that throttled the
// listing is left until the time it named.
func TestACollectionRestoredOnItsServerIsListedAgain(t *testing.T) {
	personal, made := DAVHome+"personal/", DAVHome+"made/"
	d := DAV(t, Calendar(personal, "Personal"), Object(personal+"a.ics", Event("a", "Alpha")))
	a, _ := SignedIn(t, User, WithDAV(d))
	login := a.Server.Logins()[0]
	options := *a.Server.Options
	options.DataDir, options.CacheDir = t.TempDir(), t.TempDir()
	srv, err := alborz.New(a.Server.Logger(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	db, err := bolt.Open(filepath.Join(t.TempDir(), "collections.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const poll = 100 * time.Millisecond
	var asked, found atomic.Int32
	var throttled atomic.Bool
	p, err := dav.NewProvider(srv, store.New(db, srv.LoginKey, srv.Keys), dav.Kind{
		Name: "caldav", Label: "calendar", Poll: poll, Holds: store.Calendar,
		Schemes:  [2]string{"caldavs", "caldav+insecure"},
		Discover: func(context.Context, string) (string, error) { return "", errors.New("no SRV records") },
		Own:      func(dav.Services) string { return "" },
		FindHome: func(_ context.Context, _ *http.Client, endpoint string) (string, error) {
			found.Add(1)
			if throttled.Load() && strings.Contains(endpoint, d.At()) {
				return "", &dav.ThrottledError{StatusCode: http.StatusTooManyRequests, At: time.Now().Add(time.Hour)}
			}
			return endpoint, nil
		},
		List: func(context.Context, *http.Client, *url.URL, []dav.Home) ([]dav.Collection, []error) {
			asked.Add(1)
			var out []dav.Collection
			for _, c := range d.Calendars() {
				out = append(out, dav.Collection{Path: d.At() + c})
			}
			return out, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	client := p.HTTPClient(ctx, login)
	listed := func(path string) bool {
		infos, err := p.Collections(ctx, login)
		if err != nil {
			t.Fatal(err)
		}
		return dav.At(infos, d.At()+path) != nil
	}
	read := func() string {
		resp, err := client.Get(dav.Base().ResolveReference(&url.URL{Path: d.At() + personal + "a.ics"}).String())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	// followed waits out the poll and a round, and for the list the round asked for.
	followed := func(path string) {
		t.Helper()
		time.Sleep(2 * poll)
		before := asked.Load()
		p.Follow(ctx, []*alborz.Login{login})
		for end := time.Now().Add(2 * time.Second); asked.Load() == before; time.Sleep(time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("the round did not ask the server for the list")
			}
		}
		for end := time.Now().Add(2 * time.Second); !listed(path); time.Sleep(time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("%s is not listed after the round", path)
			}
		}
	}
	if !listed(personal) || !strings.Contains(read(), "Alpha") {
		t.Fatal("the calendar and its event are not there to begin with")
	}
	c, err := webdav.NewClient(client, dav.Base().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveAll(ctx, d.At()+personal); err != nil {
		t.Fatal(err)
	}
	p.Forget(login.Username())
	if listed(personal) {
		t.Fatal("the deleted calendar is still listed")
	}

	d.Apply(Gone())
	followed(personal)
	if got := read(); !strings.Contains(got, "Alpha") {
		t.Errorf("the restored calendar's event reads %q", got)
	}

	d.Apply(Calendar(made, "Made"))
	followed(made)

	// No page asks from here on, so only the round has heard the throttle.
	throttled.Store(true)
	time.Sleep(2 * poll)
	p.Follow(ctx, []*alborz.Login{login})
	for end := time.Now().Add(2 * time.Second); len(p.Troubles(login.Username())) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the round did not hear the throttle")
		}
	}
	time.Sleep(2 * poll)
	before := found.Load()
	p.Follow(ctx, []*alborz.Login{login})
	time.Sleep(poll)
	if n := found.Load() - before; n != 0 {
		t.Errorf("the round asked a throttled account's servers %d times before the time named", n)
	}
}
