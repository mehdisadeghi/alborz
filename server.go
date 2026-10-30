package alborz

import (
	"cmp"
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

	"git.mehdix.org/alborz/internal/keyring"
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
	credentialCookieLife = 60 * 24 * time.Hour
	// reminderTick makes a reminder late by at most a minute, the finest step a calendar offers one in.
	reminderTick = time.Minute
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

	domains        map[string]*domainUpstreams
	domainsMu      sync.RWMutex
	setupMu        sync.Mutex
	setupCode      string
	setupProviders map[string]SetupProvider

	// OnAccountGone is called when an account signs out.
	OnAccountGone []func(username string)
	// OnSetup starts service discovery after the first provider is commissioned.
	OnSetup []func()

	// Archive are the features' shares of an account's archive, beside its settings.
	Archive []ArchivePart

	// Keys holds the accounts' own keys and key pairs (ADR 72); Sealers are the features keeping values
	// under them.
	Keys    *keyring.Ring
	Sealers []Sealer

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

	// Pushes keeps the devices notified and the key signing what they are sent.
	Pushes *Pushes
	// KeepLogin holds a login past its idle limit while something needs it: a device to notify (ADR 74).
	KeepLogin func(*Login) bool

	// Instance names this alborz to another asking whether a DAV server is it; see InstanceHeader.
	Instance string

	signInRefused *refusals
	data          kv.DB
	// DataPath is where data lives, for its messages.
	DataPath string

	loginFailures sync.Map
	// unlockRefused holds when a phone's password last did not open its account's key.
	unlockRefused sync.Map
}

type domainUpstreams struct {
	// An empty URL scheme requests discovery; a nil URL disables the service.
	services map[string]*url.URL

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
	if s.Options.LoginIdle == 0 {
		s.Options.LoginIdle = LoginDuration
	}
	if s.Options.ReminderTick == 0 {
		s.Options.ReminderTick = reminderTick
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
	ready := false
	defer func() {
		if !ready {
			s.data.Close()
		}
	}()
	sessionsKey, settingsKey := s.LoginKey.Key(sessionsOwner), s.LoginKey.Key(settingsOwner)
	err = CheckKey(s.data, s.DataPath, s.LoginKey, map[string]*seal.Key{sessionsOwner + "/": sessionsKey, settingsOwner + "/": settingsKey})
	if err != nil {
		return nil, err
	}
	s.SessionRecords = openSessionRecords(s.data, sessionsKey)
	if err := s.SessionRecords.Sweep(time.Now()); err != nil {
		return nil, err
	}
	if s.Keys, err = keyring.New(s.data, s.LoginKey); err != nil {
		return nil, fmt.Errorf("%s: the key pairs do not read: %w", s.DataPath, err)
	}
	if s.Pushes, err = openPushes(s.data, s.LoginKey, s.Keys, s.DataPath); err != nil {
		return nil, err
	}
	s.Sealers = append(s.Sealers, Sealer{Reseal: s.Pushes.reseal, Drop: s.Pushes.drop})
	settings := openSettingsRecords(s.data, settingsKey, s.LoginKey.Names(), s.Keys)

	s.domains = make(map[string]*domainUpstreams)
	if len(options.Domains) > 0 && options.Provider != nil {
		return nil, errors.New("cannot mix provider-wide servers with domain-specific servers")
	}
	for domain, config := range options.Domains {
		if domain == "" {
			return nil, errors.New("domain name must not be empty; use provider for all domains")
		}
		d, err := parseDomainConfig(domain, config)
		if err != nil {
			return nil, err
		}
		s.domains[domain] = d
	}
	if options.Provider != nil {
		d, err := parseDomainConfig("", options.Provider)
		if err != nil {
			return nil, err
		}
		s.domains[""] = d
	}
	needsSetup := len(s.domains) == 0

	// Each lookup can wait out a resolver's timeout, so all run at once.
	names := s.Domains()
	errs := make([][4]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		for j, parse := range []func(string) error{s.parseIMAPUpstream, s.parseSMTPUpstream, s.parseSieveUpstream, s.parsePasswdUpstream} {
			wg.Go(func() { errs[i][j] = parse(name) })
		}
	}
	wg.Wait()
	for i, name := range names {
		// An unreachable domain must not take the whole server down.
		if err := cmp.Or(errs[i][0], errs[i][1]); err != nil {
			s.log.Printf("Warning: dropping domain %q: %v", name, err)
			delete(s.domains, name)
			continue
		}
		if err := cmp.Or(errs[i][2], errs[i][3]); err != nil {
			return nil, err
		}
	}
	if len(s.domains) == 0 && !needsSetup {
		return nil, fmt.Errorf("no usable domains left")
	}

	s.logins = newLoginManager(s.dialIMAP, s.dialIMAPTransfer, s.dialIMAPWatch, s.dialSMTP, s.dialSieve, log, settings, s.Keys, s.unlock, s.Options.LoginIdle,
		func(l *Login) bool { return s.KeepLogin != nil && s.KeepLogin(l) })
	if needsSetup {
		s.startSetup()
	}
	ready = true
	return s, nil
}

// Logins are the logins alive, an account once for each browser signed in to it.
func (s *Server) Logins() []*Login { return s.logins.live() }

// Logger returns this server's logger.
func (s *Server) Logger() *log.Logger { return s.log }

// Domains lists the served mail domains in stable order.
func (s *Server) Domains() []string {
	s.domainsMu.RLock()
	defer s.domainsMu.RUnlock()
	names := make([]string, 0, len(s.domains))
	for name := range s.domains {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ErrUnreadable marks data a server sent that does not parse: expected
// input, which a page names rather than failing on (502).
var ErrUnreadable = errors.New("the server sent data that does not parse")

// ErrSuperseded marks a search a newer one of the same account replaced (409).
var ErrSuperseded = errors.New("a newer search replaced this one")

// ErrNotFound marks errors the HTTP layer answers with 404.
var ErrNotFound = errors.New("not found")

type NotFoundError struct {
	Key  string
	Args []any
}

var notFoundMessages = map[string]string{
	"notfound.addressbook": "no address books found",
	"notfound.calendar":    "no calendars found",
	"notfound.collection":  "collection not found",
	"notfound.contact":     "contact not found",
	"notfound.entry":       "journal entry not found",
	"notfound.event":       "event not found",
	"notfound.folder":      "folder %q not found",
	"notfound.invitation":  "invitation not found",
	"notfound.message":     "message %s not found in folder",
	"notfound.page":        "page not found",
	"notfound.part":        "part not found in message %s",
	"notfound.passkey":     "passkey not found",
	"notfound.reply":       "automatic reply not found",
	"notfound.rule":        "filter rule not found",
	"notfound.script":      "filter script %q not found",
	"notfound.server":      "server not found",
	"notfound.share":       "share not found",
	"notfound.signature":   "signature %q not found",
	"notfound.task":        "task not found",
}

func (e *NotFoundError) Error() string {
	if message, ok := notFoundMessages[e.Key]; ok {
		return fmt.Sprintf(message, e.Args...)
	}
	return "resource not found"
}
func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// NotFound builds a not-found error naming the missing subject by its locale key.
func NotFound(key string, args ...any) error {
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

func hostWithDefaultPort(host string, secure bool, securePort, plainPort string) string {
	if strings.ContainsRune(host, ':') {
		return host
	}
	if secure {
		return host + ":" + securePort
	}
	return host + ":" + plainPort
}

type NoUpstreamError struct {
	service string
}

func (err *NoUpstreamError) Error() string {
	return fmt.Sprintf("no %s server configured", err.service)
}

func (s *Server) upstreamsFor(domain string) (*domainUpstreams, bool) {
	s.domainsMu.RLock()
	defer s.domainsMu.RUnlock()
	d, ok := s.domains[domain]
	if !ok {
		d, ok = s.domains[""]
	}
	return d, ok
}

// Upstream returns the service URL; an empty Scheme requests discovery using Host.
func (s *Server) Upstream(domain, service string) (*url.URL, error) {
	d, ok := s.upstreamsFor(domain)
	if !ok {
		return nil, UnknownDomainError{domain}
	}
	if u := d.services[service]; u != nil {
		return u, nil
	}
	return nil, &NoUpstreamError{service}
}

func (s *Server) parseIMAPUpstream(domain string) error {
	d := s.domains[domain]

	u, err := s.Upstream(domain, "imap")
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

	d.imap.host = hostWithDefaultPort(u.Host, u.Scheme == "imaps", "993", "143")

	if s.Options.Debug {
		s.log.Printf("Domain %q: configured upstream IMAP server: %v", domain, u)
	}

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

	u, err := s.Upstream(domain, "smtp")
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
		if u == nil {
			if s.Options.Debug {
				s.log.Printf("Domain %q: publishes no SMTP server", domain)
			}
			return nil
		}
	}

	switch u.Scheme {
	case "smtps":
		d.smtp.tls = true
	case "smtp+insecure":
		d.smtp.insecure = true
	}

	d.smtp.host = hostWithDefaultPort(u.Host, u.Scheme == "smtps", "465", "587")

	if s.Options.Debug {
		s.log.Printf("Domain %q: configured upstream SMTP server: %v", domain, u)
	}

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
	Domains  map[string]DomainConfig
	Provider DomainConfig
	// SaveSetup persists verified first-run configuration before it becomes active.
	SaveSetup func(map[string]DomainConfig) error
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
	// ReminderTick paces the round that fires event reminders; zero is reminderTick.
	ReminderTick time.Duration
	// LoginIdle is how long a login outlives its last request; zero is LoginDuration.
	LoginIdle time.Duration
}

// CacheFor is how long a cache keeps what it holds: d, or zero with caching off.
func (o *Options) CacheFor(d time.Duration) time.Duration {
	if !o.Cache {
		return 0
	}
	return d
}
