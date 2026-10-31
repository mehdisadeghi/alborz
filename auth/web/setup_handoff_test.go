package web_test

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// Keep the fixture CA out of the other HTTP tests' process-wide trust store.
func TestSetupRedirectsToLogin(t *testing.T) {
	if os.Getenv("ALBORZ_SETUP_HTTP_TRUST_HELPER") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSetupRedirectsToLogin$", "-test.v")
		cmd.Env = append(os.Environ(), "ALBORZ_SETUP_HTTP_TRUST_HELPER=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("HTTP setup handoff: %v\n%s", err, out)
		}
		return
	}
	certificate := httptest.NewTLSServer(http.NotFoundHandler())
	pool := x509.NewCertPool()
	pool.AddCert(certificate.Certificate())
	secure := certificate.TLS.Clone()
	secure.NextProtos = nil
	certificate.Close()
	t.Setenv("GODEBUG", "x509usefallbackroots=1")
	x509.SetFallbackRoots(pool)

	for _, partial := range []bool{false, true} {
		form := setupTLSForm(t, secure)
		a := Alborz(t, Unconfigured())
		c := a.Client()
		form.Set("setup-code", a.Server.SetupCode())
		actions := []string{"finish"}
		if partial {
			actions = []string{"add", "discover", "finish"}
		}
		for _, action := range actions {
			posted := url.Values{"action": {action}, "setup-code": {a.Server.SetupCode()}}
			if action == "discover" {
				posted.Set("username", form.Get("username"))
			} else {
				form.Set("action", action)
				posted = form
			}
			req, err := http.NewRequest(http.MethodPost, a.URL+a.Path("auth.setup"), strings.NewReader(posted.Encode()))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			status, redirect := http.StatusFound, "Location"
			if partial {
				req.Header.Set("HX-Request", "true")
				req.Header.Set("HX-Boosted", "true")
				status, redirect = http.StatusOK, "HX-Redirect"
			}
			resp := c.Do(t, req)
			switch action {
			case "add":
				if resp.StatusCode != http.StatusOK || len(a.Server.Domains()) != 0 {
					t.Fatalf("adding another domain did not keep the first domain pending: %s", resp.Status)
				}
				a.Client().Get(t, "auth.setup")
				if len(a.Server.SetupDomains(a.Server.SetupCode())) != 1 {
					t.Fatal("another browser cleared the owner's draft")
				}
				if page := c.Get(t, "auth.setup"); page.StatusCode != http.StatusOK || len(a.Server.SetupDomains(a.Server.SetupCode())) != 0 {
					t.Fatal("refresh did not clear the entire setup draft")
				}
				if resp := c.Post(t, "auth.setup", form); resp.StatusCode != http.StatusOK {
					t.Fatalf("could not restart setup: %s", resp.Status)
				}
				form.Set("username", "owner@other.example")
			case "discover":
				if resp.StatusCode != http.StatusUnprocessableEntity {
					t.Fatalf("discovery failure answered %s", resp.Status)
				}
			case "finish":
				if resp.StatusCode != status || resp.Header.Get(redirect) != a.Path("auth.login") {
					t.Fatalf("setup did not return to login: %s, %s=%q", resp.Status, redirect, resp.Header.Get(redirect))
				}
				form = posted
			}
		}
		if a.Server.NeedsSetup() || len(a.Server.Logins()) != 0 {
			t.Fatal("setup remained open or signed in an account")
		}
		if partial && len(a.Server.Domains()) != 2 {
			t.Fatal("finishing setup lost a pending domain")
		}
		if resp := c.Post(t, "auth.setup", form); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("old owner code can still configure the instance: %s", resp.Status)
		}
		c.SignIn(t, User)
		if resp := c.Get(t, "settings.profile"); resp.StatusCode != http.StatusOK {
			t.Fatalf("configured provider cannot sign in normally: %s", resp.Status)
		}
	}
}

func setupTLSForm(t *testing.T, secure *tls.Config) url.Values {
	t.Helper()
	imap := setupTLSProxy(t, secure, IMAP(t).Addr)
	smtp := setupTLSProxy(t, secure, SMTP(t).Addr)
	imapHost, imapPort, _ := net.SplitHostPort(imap)
	smtpHost, smtpPort, _ := net.SplitHostPort(smtp)
	return url.Values{
		"action": {"finish"}, "username": {User},
		"imap-host": {imapHost}, "imap-port": {imapPort}, "imap-security": {"imaps"},
		"smtp-host": {smtpHost}, "smtp-port": {smtpPort}, "smtp-security": {"smtps"},
	}
}

func setupTLSProxy(t *testing.T, secure *tls.Config, upstream string) string {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", secure)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				remote, err := net.Dial("tcp", upstream)
				if err != nil {
					return
				}
				defer remote.Close()
				go func() { io.Copy(remote, conn); remote.Close() }()
				io.Copy(conn, remote)
			}()
		}
	}()
	return listener.Addr().String()
}
