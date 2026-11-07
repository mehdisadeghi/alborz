package alborz

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-smtp"
	"golang.org/x/net/dns/dnsmessage"
)

func newSetupServer(t *testing.T, options Options) *Server {
	t.Helper()
	options.LoginKey = "YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA="
	if options.DataDir == "" {
		options.DataDir = t.TempDir()
	}
	if options.SaveSetup == nil {
		options.SaveSetup = func(map[string]DomainConfig, SetupSite) error { return nil }
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
	s := newSetupServer(t, Options{SaveSetup: func(domains map[string]DomainConfig, _ SetupSite) error { saved = domains; return nil }})
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
	if err := s.FinishSetup(code, SetupSite{}); err != nil {
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
	if err := s.FinishSetup(code, SetupSite{}); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("completed setup accepted another finish: %v", err)
	}
	if _, err := s.DiscoverSetup(context.Background(), code, "me@example.org"); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("completed setup still permits discovery: %v", err)
	}
}

func TestSetupSaveFailureKeepsOwnerCodeAndDomainsUnchanged(t *testing.T) {
	failure := errors.New("disk full")
	s := newSetupServer(t, Options{SaveSetup: func(map[string]DomainConfig, SetupSite) error { return failure }})
	code := s.SetupCode()
	if err := s.stageSetup(code, setupTestProvider("example.org")); err != nil {
		t.Fatal(err)
	}
	err := s.FinishSetup(code, SetupSite{})
	if !errors.Is(err, failure) {
		t.Fatalf("save failure lost: %v", err)
	}
	if s.SetupCode() != code || !s.NeedsSetup() || len(s.Domains()) != 0 || !slices.Equal(s.SetupDomains(code), []string{"example.org"}) {
		t.Fatal("failed save partially configured server")
	}
	failure = nil
	if err := s.FinishSetup(code, SetupSite{}); err != nil {
		t.Fatalf("retry lost pending domains: %v", err)
	}
	if !slices.Equal(s.Domains(), []string{"example.org"}) {
		t.Fatal("retry did not publish the pending domain")
	}
}

func TestSetupConcurrentCompletionPublishesDomainsOnce(t *testing.T) {
	var saved map[string]DomainConfig
	var writes int
	s := newSetupServer(t, Options{SaveSetup: func(domains map[string]DomainConfig, _ SetupSite) error { saved = domains; writes++; return nil }})
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
		wg.Go(func() { errs <- s.FinishSetup(code, SetupSite{}) })
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
	if err := s.FinishSetup("wrong", SetupSite{}); !errors.Is(err, ErrSetupCode) {
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

func TestSetupDiscoveryIsOwnerGatedAndKeepsPartialResults(t *testing.T) {
	var queries atomic.Int64
	resolver := &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		queries.Add(1)
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			var length [2]byte
			if _, err := io.ReadFull(server, length[:]); err != nil {
				return
			}
			body := make([]byte, binary.BigEndian.Uint16(length[:]))
			if _, err := io.ReadFull(server, body); err != nil {
				return
			}
			var query dnsmessage.Message
			if err := query.Unpack(body); err != nil {
				return
			}
			response := dnsmessage.Message{ID: query.ID, Response: true, Authoritative: true, Questions: query.Questions}
			for _, q := range query.Questions {
				if q.Name.String() == "_imaps._tcp.example.org." {
					response.Answers = append(response.Answers, dnsmessage.Resource{
						Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeSRV, Class: dnsmessage.ClassINET},
						Body:   &dnsmessage.SRVResource{Port: 993, Target: dnsmessage.MustNewName("mail.example.org.")},
					})
				}
			}
			body, err := response.Pack()
			if err != nil {
				return
			}
			binary.BigEndian.PutUint16(length[:], uint16(len(body)))
			server.Write(append(length[:], body...))
		}()
		return client, nil
	}}
	previous := net.DefaultResolver
	net.DefaultResolver = resolver
	t.Cleanup(func() { net.DefaultResolver = previous })
	s := newSetupServer(t, Options{})
	if _, err := s.DiscoverSetup(context.Background(), "wrong", "me@example.org"); !errors.Is(err, ErrSetupCode) {
		t.Fatal(err)
	}
	if queries.Load() != 0 {
		t.Fatal("unauthorized discovery queried DNS")
	}
	p, err := s.DiscoverSetup(context.Background(), s.SetupCode(), "me@example.org")
	var field *SetupError
	if !errors.As(err, &field) || field.Field != "discovery" {
		t.Fatalf("missing SMTP was not reported: %v", err)
	}
	if p.Domain != "example.org" || p.IMAP != "imaps://mail.example.org:993" || p.SMTP != "" {
		t.Fatalf("partial discovery lost usable IMAP: %+v", p)
	}
	if queries.Load() == 0 || !s.NeedsSetup() || len(s.Domains()) != 0 {
		t.Fatal("discovery changed configuration or did not use DNS")
	}
}

// A separate process keeps the fixture CA out of every other test's trust store.
func TestSetupChecksConnectionsWithoutAuthentication(t *testing.T) {
	if os.Getenv("ALBORZ_SETUP_TRUST_HELPER") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSetupChecksConnectionsWithoutAuthentication$", "-test.v")
		cmd.Env = append(os.Environ(), "ALBORZ_SETUP_TRUST_HELPER=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("TLS setup integration: %v\n%s", err, out)
		}
		return
	}
	certificate := httptest.NewTLSServer(http.NotFoundHandler())
	pool := x509.NewCertPool()
	pool.AddCert(certificate.Certificate())
	secure := certificate.TLS.Clone()
	certificate.Close()
	t.Setenv("GODEBUG", "x509usefallbackroots=1")
	x509.SetFallbackRoots(pool)

	for _, mode := range []string{"implicit TLS", "STARTTLS"} {
		t.Run(mode, func(t *testing.T) {
			s := newSetupServer(t, Options{})
			p, outgoing := setupMailServers(t, secure, mode == "implicit TLS")
			if err := s.AddSetup(context.Background(), s.SetupCode(), "me@example.org", p); err != nil {
				t.Fatal(err)
			}
			if !s.NeedsSetup() || len(s.Domains()) != 0 {
				t.Fatal("connection checks published domains before finishing")
			}
			if err := s.FinishSetup(s.SetupCode(), SetupSite{}); err != nil {
				t.Fatal(err)
			}
			if s.NeedsSetup() {
				t.Fatal("successful connection checks left setup open")
			}
			if outgoing.Load() != 0 {
				t.Fatal("setup attempted to send mail")
			}
		})
	}
}

func setupMailServers(t *testing.T, secure *tls.Config, implicit bool) (SetupProvider, *atomic.Int64) {
	t.Helper()
	listen := func() net.Listener {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if implicit {
			return tls.NewListener(ln, secure)
		}
		return ln
	}
	mem := imapmemserver.New()
	imap := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		TLSConfig: secure,
	})
	imapListener := listen()
	go imap.Serve(imapListener)
	t.Cleanup(func() { imap.Close() })
	backend := &setupSMTPBackend{}
	submission := smtp.NewServer(backend)
	submission.Domain, submission.TLSConfig = "localhost", secure
	smtpListener := listen()
	go submission.Serve(smtpListener)
	t.Cleanup(func() { submission.Close() })
	imapScheme, smtpScheme := "imap", "smtp"
	if implicit {
		imapScheme, smtpScheme = "imaps", "smtps"
	}
	return SetupProvider{Domain: "example.org", IMAP: imapScheme + "://" + imapListener.Addr().String(), SMTP: smtpScheme + "://" + smtpListener.Addr().String()}, &backend.outgoing
}

type setupSMTPBackend struct {
	outgoing atomic.Int64
}

func (s *setupSMTPBackend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return setupSMTPSession{s}, nil
}

type setupSMTPSession struct{ s *setupSMTPBackend }

func (s setupSMTPSession) Mail(string, *smtp.MailOptions) error {
	s.s.outgoing.Add(1)
	return errors.New("setup must not send mail")
}
func (s setupSMTPSession) Rcpt(string, *smtp.RcptOptions) error {
	return errors.New("setup must not send mail")
}
func (s setupSMTPSession) Data(io.Reader) error {
	s.s.outgoing.Add(1)
	return errors.New("setup must not send mail")
}
func (s setupSMTPSession) Reset()        {}
func (s setupSMTPSession) Logout() error { return nil }

func TestSetupRejectsUntrustedTLSWithoutSaving(t *testing.T) {
	s := newSetupServer(t, Options{})
	remote := httptest.NewTLSServer(http.NotFoundHandler())
	defer remote.Close()
	p := setupTestProvider("example.org")
	p.IMAP = strings.Replace(remote.URL, "https:", "imaps:", 1)
	err := s.AddSetup(context.Background(), s.SetupCode(), "me@example.org", p)
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); !ok {
		t.Fatalf("expected TLS verification failure, got %v", err)
	}
	if !s.NeedsSetup() || len(s.Domains()) != 0 {
		t.Fatal("untrusted service configured server")
	}
}

func TestSetupCancellationInterruptsStalledTLS(t *testing.T) {
	s := newSetupServer(t, Options{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		conn, err := ln.Accept()
		if err == nil {
			defer conn.Close()
			io.Copy(io.Discard, conn)
		}
	}()
	p := setupTestProvider("example.org")
	p.IMAP = "imaps://" + ln.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = s.AddSetup(ctx, s.SetupCode(), "me@example.org", p)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("stalled TLS did not stop with request context: %v", err)
	}
	if !s.NeedsSetup() || len(s.Domains()) != 0 {
		t.Fatal("canceled verification configured server")
	}
	<-finished
}
