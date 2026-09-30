package alborz

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/seal"
)

// BrandName is the name a person reads.
const BrandName = "Alborz"

// AppName is the name the machine reads, such as the data and cache directory.
const AppName = "alborz"

// InstanceHeader carries Server.Instance on every DAV answer, so a server found by discovery can be told from this one.
const InstanceHeader = "Alborz-Instance"

// Build returns the version the go tool stamped and a short form for the head.
var Build = sync.OnceValues(func() (full, short string) {
	info, _ := debug.ReadBuildInfo()
	full = info.Main.Version
	for _, s := range info.Settings {
		if s.Key != "vcs.revision" {
			continue
		}
		// A pseudo-version ends in the commit's first twelve digits.
		if _, mark, ok := strings.Cut(full, "-"+s.Value[:12]); ok {
			return full, s.Value[:7] + mark
		}
	}
	return full, full
})

const storeFile = AppName + ".db"

const (
	credentialCookieLife = 30 * 24 * time.Hour
)

// CredentialCookieLife is how long a remembered account is kept.
func CredentialCookieLife() time.Duration { return credentialCookieLife }

// Server holds all the alborz server state.
type Server struct {
	log     *log.Logger
	logins  *loginManager
	Options *Options

	// LoginKey and PublicURL are the options read.
	LoginKey  seal.Root
	PublicURL *url.URL

	domains map[string]*domainUpstreams

	// OnAccountGone is called when an account signs out.
	OnAccountGone []func(username string)

	// OnClose releases what a feature holds open.
	OnClose []func() error

	// Changes carries what the watchers saw to the open streams.
	Changes *Changes

	// Outgoing is how every request to another server leaves.
	Outgoing Outgoing

	// Schedule runs what keeps its own time.
	Schedule *Schedule

	// SessionRecords keeps the browsers signed in across a restart.
	SessionRecords SessionRecords

	// Instance names this alborz to another asking whether a DAV server is it; see InstanceHeader.
	Instance string

	signInRefused *refusals
	data          kv.DB
	// DataPath is where data lives, for its messages.
	DataPath string

	loginFailures sync.Map
}

type domainUpstreams struct {
	// protocol can be empty for auto-discovery
	upstreams map[string]*url.URL

	// found is the SRV record a server was found at, empty where the deployment named it.
	imap struct {
		host     string
		tls      bool
		insecure bool
		found    string
	}
	smtp struct {
		host     string
		tls      bool
		insecure bool
		found    string
	}
	sieve struct {
		host     string
		insecure bool
		found    string
	}
	passwd passwdServer
}

// New opens the data file and the upstreams the options name.
func New(log *log.Logger, options Options) (*Server, error) {
	s := &Server{log: log, Options: &options,
		Changes: newChanges(), Outgoing: NewOutgoing(), Schedule: newSchedule(), signInRefused: newRefusals(signInTries, signInWindow)}
	if options.Guarded != nil {
		s.Outgoing.Guarded = options.Guarded
	}
	if options.LoginKey == "" {
		return nil, errors.New("no login key: make one with `openssl rand -base64 32`")
	}
	var err error
	if s.LoginKey, err = seal.ParseRoot(options.LoginKey); err != nil {
		return nil, fmt.Errorf("invalid login key: %v", err)
	}
	if options.PublicURL != "" {
		u, err := url.Parse(options.PublicURL)
		// alborz answers at the root of its host only.
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.Trim(u.Path, "/") != "" {
			return nil, fmt.Errorf("invalid public URL %q: want scheme://host", options.PublicURL)
		}
		s.PublicURL = u
	}

	// Absolute, so messages name a path without guessing the working directory; empty is the working directory.
	dataDir, err := filepath.Abs(options.DataDir)
	if err != nil {
		return nil, err
	}
	version, _ := Build()
	s.DataPath = filepath.Join(dataDir, storeFile)
	if s.data, s.Instance, err = openData(dataDir, version, log.Printf); err != nil {
		return nil, err
	}
	sessionsKey, settingsKey := s.LoginKey.Key(sessionsOwner), s.LoginKey.Key(settingsOwner)
	err = CheckKey(s.data, s.DataPath, s.LoginKey, map[string]*seal.Key{sessionsOwner + "/": sessionsKey, settingsOwner + "/": settingsKey})
	if err != nil {
		return nil, err
	}
	s.SessionRecords = openSessionRecords(s.data, sessionsKey)
	if err := s.SessionRecords.Sweep(time.Now().Add(-credentialCookieLife)); err != nil {
		return nil, err
	}
	settings := openSettingsRecords(s.data, settingsKey, s.LoginKey.Names())

	s.domains = make(map[string]*domainUpstreams)
	for _, arg := range options.Upstreams {
		name, spec, explicit := strings.Cut(arg, "=")
		if !explicit && strings.Contains(arg, "://") {
			// A plain URL configures the unnamed provider, which accepts any domain.
			name, spec = "", arg
		} else if name == "" || strings.ContainsAny(name, ":/") {
			return nil, fmt.Errorf("invalid upstream %q: expected url, domain or domain=url", arg)
		} else if !explicit {
			spec = name
		}
		u, err := parseUpstream(spec)
		if err != nil {
			return nil, fmt.Errorf("failed to parse upstream %q: %v", arg, err)
		}
		d, ok := s.domains[name]
		if !ok {
			d = &domainUpstreams{upstreams: make(map[string]*url.URL)}
			s.domains[name] = d
		}
		if _, ok := d.upstreams[u.Scheme]; ok {
			return nil, fmt.Errorf("domain %q: found two upstream servers for scheme %q", name, u.Scheme)
		}
		d.upstreams[u.Scheme] = u
	}
	if len(s.domains) == 0 {
		return nil, fmt.Errorf("no upstreams specified")
	}
	if _, ok := s.domains[""]; ok && len(s.domains) > 1 {
		return nil, fmt.Errorf("cannot mix plain upstream URLs with domains")
	}

	for _, name := range s.Domains() {
		// An unreachable domain must not take the whole server down.
		if err := s.parseIMAPUpstream(name); err != nil {
			s.log.Printf("Warning: dropping domain %q: %v", name, err)
			delete(s.domains, name)
			continue
		}
		if err := s.parseSMTPUpstream(name); err != nil {
			s.log.Printf("Warning: dropping domain %q: %v", name, err)
			delete(s.domains, name)
			continue
		}
		if err := s.parseSieveUpstream(name); err != nil {
			return nil, err
		}
		if err := s.parsePasswdUpstream(name); err != nil {
			return nil, err
		}
	}
	if len(s.domains) == 0 {
		return nil, fmt.Errorf("no usable domains left")
	}

	s.logins = newLoginManager(s.dialIMAP, s.dialIMAPTransfer, s.dialIMAPWatch, s.dialSMTP, s.dialSieve, log, settings)
	return s, nil
}

// Logger returns this server's logger.
func (s *Server) Logger() *log.Logger { return s.log }

// Domains lists the served mail domains in stable order.
func (s *Server) Domains() []string {
	names := make([]string, 0, len(s.domains))
	for name := range s.domains {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ErrNotFound marks errors the HTTP layer answers with 404.
var ErrNotFound = errors.New("not found")

type NotFoundError struct {
	Key  string
	Args []interface{}
}

func (e *NotFoundError) Error() string { return fmt.Sprintf("%s %v", e.Key, e.Args) }
func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// NotFound builds a not-found error naming the missing subject by its locale key.
func NotFound(key string, args ...interface{}) error {
	return &NotFoundError{key, args}
}

// UnknownDomainError is returned when a login address does not belong to a served domain.
type UnknownDomainError struct {
	Domain string
}

func (err UnknownDomainError) Error() string {
	if err.Domain == "" {
		return "log in with a full user@domain address"
	}
	return fmt.Sprintf("mail domain %q is not served here", err.Domain)
}

func (s *Server) Close() {
	s.Schedule.close()
	s.logins.close()
	s.data.Close()
	for _, release := range s.OnClose {
		if err := release(); err != nil {
			s.log.Printf("close: %v", err)
		}
	}
}

func parseUpstream(s string) (*url.URL, error) {
	if !strings.ContainsAny(s, ":/") {
		s = "//" + s
	}
	return url.Parse(s)
}

type NoUpstreamError struct {
	schemes []string
}

func (err *NoUpstreamError) Error() string {
	return fmt.Sprintf("no upstream server configured for schemes %v", err.schemes)
}

func (s *Server) upstreamsFor(domain string) (*domainUpstreams, bool) {
	d, ok := s.domains[domain]
	if !ok {
		d, ok = s.domains[""]
	}
	return d, ok
}

// Upstream returns the domain's upstream URL for schemes; an empty Scheme means auto-discover with Host.
func (s *Server) Upstream(domain string, schemes ...string) (*url.URL, error) {
	d, ok := s.upstreamsFor(domain)
	if !ok {
		return nil, UnknownDomainError{domain}
	}
	var urls []*url.URL
	for _, scheme := range schemes {
		if u, ok := d.upstreams[scheme]; ok {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		if u, ok := d.upstreams[""]; ok {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return nil, &NoUpstreamError{schemes}
	}
	if len(urls) > 1 {
		return nil, fmt.Errorf("multiple upstream servers are configured for schemes %v", schemes)
	}
	return urls[0], nil
}

func (s *Server) parseIMAPUpstream(domain string) error {
	d := s.domains[domain]

	u, err := s.Upstream(domain, "imap", "imaps", "imap+insecure")
	if err != nil {
		return fmt.Errorf("domain %q: failed to parse upstream IMAP server: %v", domain, err)
	}

	if u.Scheme == "" {
		u, d.imap.found, err = discoverIMAP(u.Host)
		if err != nil {
			return fmt.Errorf("domain %q: failed to discover IMAP server: %v", domain, err)
		}
	}

	switch u.Scheme {
	case "imaps":
		d.imap.tls = true
	case "imap+insecure":
		d.imap.insecure = true
	}

	d.imap.host = u.Host
	if !strings.ContainsRune(d.imap.host, ':') {
		if u.Scheme == "imaps" {
			d.imap.host += ":993"
		} else {
			d.imap.host += ":143"
		}
	}

	s.log.Printf("Domain %q: configured upstream IMAP server: %v", domain, u)

	c, err := s.dialIMAP(domain, "")
	if err != nil {
		s.log.Printf("Warning: IMAP server %v not reachable at startup: %v", u, err)
	} else {
		c.Close()
	}
	return nil
}

func (s *Server) parseSMTPUpstream(domain string) error {
	d := s.domains[domain]

	u, err := s.Upstream(domain, "smtp", "smtps", "smtp+insecure")
	if _, ok := err.(*NoUpstreamError); ok {
		return nil
	} else if err != nil {
		return fmt.Errorf("domain %q: failed to parse upstream SMTP server: %v", domain, err)
	}

	if u.Scheme == "" {
		u, d.smtp.found, err = discoverSMTP(u.Host)
		if err != nil {
			s.log.Printf("Domain %q: failed to discover SMTP server: %v", domain, err)
			return nil
		}
	}

	switch u.Scheme {
	case "smtps":
		d.smtp.tls = true
	case "smtp+insecure":
		d.smtp.insecure = true
	}

	d.smtp.host = u.Host
	if !strings.ContainsRune(d.smtp.host, ':') {
		if u.Scheme == "smtps" {
			d.smtp.host += ":465"
		} else {
			d.smtp.host += ":587"
		}
	}

	s.log.Printf("Domain %q: configured upstream SMTP server: %v", domain, u)

	c, err := s.dialSMTP(domain)
	if err != nil {
		s.log.Printf("Warning: SMTP server %v not reachable at startup: %v", u, err)
	} else {
		c.Close()
	}
	return nil
}

// ForgetAccount tells every feature that asked that an account has signed out.
func (s *Server) ForgetAccount(username string) {
	for _, forget := range s.OnAccountGone {
		forget(username)
	}
}

type Options struct {
	Upstreams []string
	// Debug logs more, and reads templates and assets from ./templates.
	Debug bool
	// LoginKey is the key, as base64 text, every key sealing what alborz keeps is derived from.
	LoginKey string
	// Cache keeps what the servers answered.
	Cache bool
	// CacheDir keeps sealed copies of synced DAV collections while Cache is on.
	CacheDir string
	// DataDir holds alborz.db; empty is the working directory.
	DataDir string
	// PrivateServices lets an account name a server on plain HTTP or a private address.
	PrivateServices bool
	// PublicURL is where readers reach alborz, scheme and host, for links in mail and for other clients;
	// empty takes each request's own.
	PublicURL string
	// Guarded carries the requests to servers outsiders name; nil dials public addresses only.
	Guarded http.RoundTripper
}

// CacheFor is how long a cache keeps what it holds: d, or zero with caching off.
func (o *Options) CacheFor(d time.Duration) time.Duration {
	if !o.Cache {
		return 0
	}
	return d
}
