package dav_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
)

type discoveryTransport func(*http.Request) (*http.Response, error)

func (f discoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
	options.Upstreams = []string{upstream + "imap+insecure://mail.example.test:143"}
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
		Discover: func(context.Context, string) (string, error) { return "", nil },
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
