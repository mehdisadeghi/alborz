package web_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestAnUnconfiguredInstanceOffersOwnerSetup(t *testing.T) {
	a := Alborz(t, Unconfigured())
	c := a.Client()
	resp := c.Get(t, "auth.login")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/setup" {
		t.Fatalf("unconfigured login: %s to %q", resp.Status, resp.Header.Get("Location"))
	}
	req, err := http.NewRequest(http.MethodGet, a.URL+"/setup", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp = c.Do(t, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("setup page: %s", resp.Status)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("setup page can be stored")
	}
	if strings.Contains(resp.Text, a.Server.SetupCode()) {
		t.Fatal("public setup page exposes the console code")
	}
}

func TestConfiguredInstancesDoNotOfferSetup(t *testing.T) {
	a := Alborz(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req, err := http.NewRequest(method, a.URL+"/setup", nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp := a.Client().Do(t, req); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("configured %s setup answered %s", method, resp.Status)
		}
	}
}

func TestSetupRequiresTheOwnerCode(t *testing.T) {
	a := Alborz(t, Unconfigured())
	for _, action := range []string{"discover", "add"} {
		resp := a.Client().Post(t, "auth.setup", url.Values{
			"action": {action}, "setup-code": {"wrong"}, "username": {"owner@example.invalid"},
		})
		if resp.StatusCode != http.StatusForbidden || !a.Server.NeedsSetup() {
			t.Fatalf("%s without owner code: %s", action, resp.Status)
		}
		if strings.Contains(resp.Text, a.Server.SetupCode()) {
			t.Fatal("refusal exposes the owner code")
		}
	}
}

func TestSetupRefusesAnEmptyIncomingServer(t *testing.T) {
	a := Alborz(t, Unconfigured())
	form := url.Values{"action": {"add"}, "setup-code": {a.Server.SetupCode()}, "username": {"owner@example.invalid"},
		"imap-host": {""}, "imap-port": {"993"}, "imap-security": {"imaps"},
		"smtp-host": {"mail.example.invalid"}, "smtp-port": {"587"}, "smtp-security": {"smtp"},
		"caldav": {"https://dav.example.invalid/calendars/"}}
	resp := a.Client().Post(t, "auth.setup", form)
	if resp.StatusCode != http.StatusUnprocessableEntity || !a.Server.NeedsSetup() {
		t.Fatalf("invalid incoming server: %s", resp.Status)
	}
}

func TestSetupRejectsCrossOriginPosts(t *testing.T) {
	a := Alborz(t, Unconfigured())
	form := url.Values{"action": {"discover"}, "setup-code": {a.Server.SetupCode()}, "username": {"owner@example.invalid"}}
	req, err := http.NewRequest(http.MethodPost, a.URL+"/setup", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://elsewhere.invalid")
	if resp := a.Client().Do(t, req); resp.StatusCode != http.StatusForbidden || !a.Server.NeedsSetup() {
		t.Fatalf("cross-origin setup answered %s", resp.Status)
	}
}

func TestSetupDiscoveryFailureHidesResolverInternals(t *testing.T) {
	for _, failure := range []error{errors.New("DNS unavailable"), context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			previous := net.DefaultResolver
			net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, failure
			}}
			t.Cleanup(func() { net.DefaultResolver = previous })
			a := Alborz(t, Unconfigured())
			resp := a.Client().Post(t, "auth.setup", url.Values{"action": {"discover"}, "setup-code": {a.Server.SetupCode()}, "username": {"owner@example.invalid"}})
			if resp.StatusCode != http.StatusUnprocessableEntity || !a.Server.NeedsSetup() {
				t.Fatalf("failed discovery: %s", resp.Status)
			}
			if strings.Contains(resp.Text, "lookup _") || strings.Contains(resp.Text, "context deadline exceeded") {
				t.Fatal("the refusal exposes resolver internals")
			}
		})
	}
}
