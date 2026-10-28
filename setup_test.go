package alborz

import (
	"context"
	"errors"
	"io"
	"log"
	"reflect"
	"slices"
	"sync"
	"testing"
)

func newSetupServer(t *testing.T, options Options) *Server {
	t.Helper()
	options.LoginKey = "YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA="
	if options.DataDir == "" {
		options.DataDir = t.TempDir()
	}
	if options.SaveSetup == nil {
		options.SaveSetup = func(map[string]DomainConfig) error { return nil }
	}
	s, err := New(log.New(io.Discard, "", 0), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func setupTestProvider(domain string) SetupProvider {
	return SetupProvider{Domain: domain, IMAP: "imaps://127.0.0.1:1", SMTP: "smtps://127.0.0.1:1"}
}

func TestSetupFinishPersistsDomainsAndClosesSetup(t *testing.T) {
	var saved map[string]DomainConfig
	s := newSetupServer(t, Options{SaveSetup: func(domains map[string]DomainConfig) error { saved = domains; return nil }})
	code := s.SetupCode()
	p := setupTestProvider("example.org")
	other := setupTestProvider("other.example")
	for _, provider := range []SetupProvider{p, other} {
		if err := s.stageSetup(code, provider); err != nil {
			t.Fatal(err)
		}
	}
	if saved != nil || !s.NeedsSetup() || len(s.Domains()) != 0 || !slices.Equal(s.SetupDomains(code), []string{p.Domain, other.Domain}) {
		t.Fatal("staging saved or published domains")
	}
	if err := s.FinishSetup(code); err != nil {
		t.Fatal(err)
	}
	if s.NeedsSetup() || s.SetupCode() != "" || !slices.Equal(s.Domains(), []string{p.Domain, other.Domain}) {
		t.Fatal("finished setup did not publish both domains")
	}
	if _, ok := s.upstreamsFor("unknown.example"); ok {
		t.Fatal("setup bypassed the domain allowlist")
	}
	if !reflect.DeepEqual(saved, map[string]DomainConfig{p.Domain: p.Services(), other.Domain: other.Services()}) {
		t.Fatalf("saved domains differ: %v", saved)
	}
	if err := s.AddSetup(context.Background(), code, "me@example.org", p); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("completed setup accepted another attempt: %v", err)
	}
	if err := s.FinishSetup(code); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("completed setup accepted another finish: %v", err)
	}
	if _, err := s.DiscoverSetup(context.Background(), code, "me@example.org"); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("completed setup still permits discovery: %v", err)
	}
}

func TestSetupSaveFailureKeepsOwnerCodeAndDomainsUnchanged(t *testing.T) {
	failure := errors.New("disk full")
	s := newSetupServer(t, Options{SaveSetup: func(map[string]DomainConfig) error { return failure }})
	code := s.SetupCode()
	if err := s.stageSetup(code, setupTestProvider("example.org")); err != nil {
		t.Fatal(err)
	}
	err := s.FinishSetup(code)
	if !errors.Is(err, failure) {
		t.Fatalf("save failure lost: %v", err)
	}
	if s.SetupCode() != code || !s.NeedsSetup() || len(s.Domains()) != 0 || !slices.Equal(s.SetupDomains(code), []string{"example.org"}) {
		t.Fatal("failed save partially configured server")
	}
	failure = nil
	if err := s.FinishSetup(code); err != nil {
		t.Fatalf("retry lost pending domains: %v", err)
	}
	if !slices.Equal(s.Domains(), []string{"example.org"}) {
		t.Fatal("retry did not publish the pending domain")
	}
}

func TestSetupConcurrentCompletionPublishesDomainsOnce(t *testing.T) {
	var saved map[string]DomainConfig
	var writes int
	s := newSetupServer(t, Options{SaveSetup: func(domains map[string]DomainConfig) error { saved = domains; writes++; return nil }})
	code := s.SetupCode()
	domains := []string{"one.example", "two.example"}
	for _, domain := range domains {
		if err := s.stageSetup(code, setupTestProvider(domain)); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { errs <- s.FinishSetup(code) })
	}
	wg.Wait()
	close(errs)
	success, closed := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrSetupClosed) {
			closed++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || closed != 1 {
		t.Fatalf("got %d winners, %d rejected attempts", success, closed)
	}
	if writes != 1 || len(saved) != 2 || !slices.Equal(s.Domains(), domains) {
		t.Fatalf("published and saved providers differ: %v (%d writes)", saved, writes)
	}
}

func TestSetupChecksOwnerBeforeInspectingOrConnecting(t *testing.T) {
	s := newSetupServer(t, Options{})
	if err := s.AddSetup(context.Background(), "wrong", "not-an-email", SetupProvider{}); !errors.Is(err, ErrSetupCode) {
		t.Fatalf("unauthorized setup returned %v", err)
	}
	if err := s.FinishSetup("wrong"); !errors.Is(err, ErrSetupCode) {
		t.Fatalf("unauthorized finish returned %v", err)
	}
	if _, err := s.DiscoverSetup(context.Background(), "wrong", "not-an-email"); !errors.Is(err, ErrSetupCode) {
		t.Fatalf("unauthorized discovery returned %v", err)
	}
}

func TestSetupRejectsUnsafeMailServerURLs(t *testing.T) {
	s := newSetupServer(t, Options{})
	for _, raw := range []string{"imap+insecure://localhost:143", "http://localhost", "imaps://user:password@localhost", "imaps://localhost/mail", "imaps://localhost?insecure=1", "imaps://localhost#fragment", "imaps://localhost:0"} {
		err := s.AddSetup(context.Background(), s.SetupCode(), "me@example.org", SetupProvider{IMAP: raw, SMTP: "smtps://localhost:465"})
		var field *SetupError
		if !errors.As(err, &field) || field.Field != "imap" {
			t.Errorf("%s returned %v", raw, err)
		}
	}
	if !s.NeedsSetup() || len(s.Domains()) != 0 {
		t.Fatal("invalid servers configured the instance")
	}
}
