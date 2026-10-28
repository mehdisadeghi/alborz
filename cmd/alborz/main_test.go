package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/alborztest"
	"github.com/BurntSushi/toml"
)

const testLoginKey = "YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA="

func startAndStop(t *testing.T, addr, key string, upstreams ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	args := append([]string{"-config", filepath.Join(t.TempDir(), "config.toml"), "-addr", addr, "-cache=false", "-data-dir", t.TempDir()}, upstreams...)
	err := run(ctx, args, func(name string) (string, bool) {
		if name == "LBRZ_LOGIN_KEY" {
			return key, key != ""
		}
		return "", false
	}, &output)
	return output.String(), err
}

func TestUnconfiguredInstanceAnnouncesOwnerSetupAfterBinding(t *testing.T) {
	output, err := startAndStop(t, "127.0.0.1:0", testLoginKey)
	if err != nil {
		t.Fatal(err)
	}
	var address, code string
	for _, line := range strings.Split(output, "\n") {
		if value, found := strings.CutPrefix(line, "First-run setup: "); found {
			address = value
		}
		if value, found := strings.CutPrefix(line, "Owner setup code: "); found {
			code = value
		}
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Port() == "0" || u.Path != "/setup" || u.RawQuery != "" {
		t.Fatalf("setup address is not the bound local listener: %q (%v)", address, err)
	}
	if code == "" || strings.Contains(address, code) {
		t.Fatal("owner code must be printed separately from the setup URL")
	}
	if strings.Contains(output, testLoginKey) {
		t.Fatal("startup output disclosed the login key")
	}
}

func TestConfiguredInstanceDoesNotAnnounceSetup(t *testing.T) {
	imap := alborztest.IMAP(t)
	output, err := startAndStop(t, "127.0.0.1:0", testLoginKey, imap.Upstream())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "First-run setup:") || strings.Contains(output, "Owner setup code:") {
		t.Fatal("a configured instance offered setup")
	}
}

func TestSetupGeneratesAndReusesLoginKey(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.toml")
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	args := []string{"-config", config, "-data-dir", dir, "-addr", "127.0.0.1:0", "-cache=false"}
	lookup := func(string) (string, bool) { return "", false }
	var output bytes.Buffer
	if err := run(ctx, args, lookup, &output); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(before, []byte("login-key = \"")) {
		t.Fatal("configuration has no generated key")
	}
	cfg := &configFile{path: config, raw: before, values: make(map[string]any)}
	domains := map[string]alborz.DomainConfig{
		"example.invalid": {"imap": "imaps://127.0.0.1:1", "smtp": "smtps://127.0.0.1:1"},
		"other.invalid":   {"imap": "imaps://127.0.0.1:2", "smtp": "smtps://127.0.0.1:2"},
	}
	if err := cfg.saveSetup(domains); err != nil {
		t.Fatal(err)
	}
	before = bytes.Clone(cfg.raw)
	output.Reset()
	if err := run(ctx, args, lookup, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "Owner setup code:") {
		t.Fatal("restart lost configured servers")
	}
	after, err := os.ReadFile(config)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("restart changed the configuration")
	}
	reloaded := &configFile{values: make(map[string]any)}
	if _, err := toml.Decode(string(after), &reloaded.values); err != nil {
		t.Fatal(err)
	}
	var options alborz.Options
	if err := reloaded.servers(&options, nil, lookup); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(options.Domains, domains) {
		t.Fatalf("restart lost configured domains: %v", options.Domains)
	}
}

func TestFailedBindDoesNotAnnounceSetup(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { occupied.Close() })
	output, err := startAndStop(t, occupied.Addr().String(), testLoginKey)
	var network *net.OpError
	if !errors.As(err, &network) || network.Op != "listen" {
		t.Fatalf("occupied address returned %v", err)
	}
	if strings.Contains(output, "First-run setup:") || strings.Contains(output, "Owner setup code:") {
		t.Fatal("setup was announced before the listener could bind")
	}
}
