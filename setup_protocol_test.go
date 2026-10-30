package alborz

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-smtp"
)

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
			if err := s.FinishSetup(s.SetupCode()); err != nil {
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
