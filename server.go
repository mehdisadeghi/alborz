package alborz

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// BrandName is what a person reads: page titles, the User-Agent on a
// message, the name given to an IMAP server. The code spells itself
// "alborz" everywhere else.
const BrandName = "Alborz"

// What a launcher paints before any stylesheet has loaded. The window
// colour is the one head.html already declares for a light scheme, so
// an installed window and a tab do not disagree; the splash behind the
// icon is the same paper.
const (
	brandColor      = "#ffffff"
	brandBackground = "#ffffff"
)

const (
	cookieName              = "alborz_session"
	accountsCookieName      = "alborz_accounts"
	activeUserCookieName    = "alborz_user"
	loginTokenCookieName    = "alborz_login_tokens"
	unifiedCookieName       = "alborz_unified"
	schemeCookieName        = "alborz_scheme"
	themeCookieName         = "alborz_theme"
	accountColorsCookieName = "alborz_account_colors"
	alignCookieName         = "alborz_align"
	textSizeCookieName      = "alborz_text"
	// TimezoneCookieName is written by the page, not by us: the browser
	// is the only party that knows its own zone.
	TimezoneCookieName = "alborz_tz"
)

// How long a cookie outlives the browser being closed. Two lives,
// because a cookie carrying a credential and one carrying a preference
// are not the same risk: the first is re-earned by signing in again,
// the second is only an annoyance to lose.
const (
	credentialCookieLife = 30 * 24 * time.Hour
	preferenceCookieLife = 365 * 24 * time.Hour
)

// Server holds all the alborz server state.
type Server struct {
	e        *echo.Echo
	Sessions *SessionManager
	Options  *Options

	mutex   sync.RWMutex // used for server reload
	plugins []Plugin

	domains map[string]*domainUpstreams

	// OnAccountReady is called when an account signs in. It is how the
	// base plugin gets to start following the mailbox without the root
	// package knowing what following is.
	OnAccountReady AccountReadyFunc

	// OnAccountGone is called when an account signs out. Plugins holding
	// anything on its behalf register here; the root package knows only
	// that the account is gone.
	OnAccountGone []func(username string)

	assetsMu sync.Mutex
	assets   map[string]assetStamp // theme asset content stamps by name
}

// assetStamp is one cached content digest; key identifies the content the
// stamp was computed from.
type assetStamp struct {
	key   string
	stamp string
}

// domainUpstreams holds the upstream servers for one served mail domain.
type domainUpstreams struct {
	// maps protocols to URLs (protocol can be empty for auto-discovery)
	upstreams map[string]*url.URL

	imap struct {
		host     string
		tls      bool
		insecure bool
	}
	smtp struct {
		host     string
		tls      bool
		insecure bool
	}
	sieve struct {
		host     string
		insecure bool
	}
}

func newServer(e *echo.Echo, options *Options) (*Server, error) {
	s := &Server{e: e, Options: options, assets: make(map[string]assetStamp)}

	s.domains = make(map[string]*domainUpstreams)
	for _, arg := range options.Upstreams {
		name, spec, explicit := strings.Cut(arg, "=")
		if !explicit && strings.Contains(arg, "://") {
			// An upstream-style plain URL configures the unnamed
			// provider, which accepts logins of any domain.
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
		// A domain without a reachable IMAP or SMTP configuration (e.g.
		// missing SRV records) must not take the whole server down with it.
		if err := s.parseIMAPUpstream(name); err != nil {
			s.e.Logger.Printf("Warning: dropping domain %q: %v", name, err)
			delete(s.domains, name)
			continue
		}
		if err := s.parseSMTPUpstream(name); err != nil {
			s.e.Logger.Printf("Warning: dropping domain %q: %v", name, err)
			delete(s.domains, name)
			continue
		}
		if err := s.parseSieveUpstream(name); err != nil {
			return nil, err
		}
	}
	if len(s.domains) == 0 {
		return nil, fmt.Errorf("no usable domains left")
	}

	s.Sessions = newSessionManager(s.dialIMAP, s.dialIMAPWatch, s.dialSMTP, s.dialSieve, e.Logger)
	s.Sessions.onGone = s.ForgetAccount
	return s, nil
}

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

// notFoundError carries the sentence the info page shows.
type notFoundError struct{ msg string }

func (e *notFoundError) Error() string { return e.msg }
func (e *notFoundError) Unwrap() error { return ErrNotFound }

// NotFoundf builds a not-found error whose text names the missing
// subject, for the info page the HTTP layer renders.
func NotFoundf(format string, args ...interface{}) error {
	return &notFoundError{fmt.Sprintf(format, args...)}
}

// UnknownDomainError is returned when a login address does not belong to a
// served domain.
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
	s.Sessions.Close()
}

func parseUpstream(s string) (*url.URL, error) {
	if !strings.ContainsAny(s, ":/") {
		// This is a raw domain name, make it an URL with an empty scheme
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

// upstreamsFor resolves the domain's provider, falling back to the unnamed
// provider, which accepts any domain.
func (s *Server) upstreamsFor(domain string) (*domainUpstreams, bool) {
	d, ok := s.domains[domain]
	if !ok {
		d, ok = s.domains[""]
	}
	return d, ok
}

// Upstream retrieves the domain's upstream server URL for the provided
// schemes. If no configured upstream server matches, a *NoUpstreamError is
// returned. An empty URL.Scheme means that the caller needs to perform
// auto-discovery with URL.Host. An explicit scheme wins over the bare
// domain, so single protocols can be redirected, e.g. IMAP to loopback on
// the mail host itself, while the rest keeps its SRV discovery.
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
		u, err = discoverIMAP(u.Host)
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

	s.e.Logger.Printf("Domain %q: configured upstream IMAP server: %v", domain, u)

	c, err := s.dialIMAP(domain)
	if err != nil {
		s.e.Logger.Printf("Warning: IMAP server %v not reachable at startup: %v", u, err)
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
		u, err = discoverSMTP(u.Host)
		if err != nil {
			s.e.Logger.Printf("Domain %q: failed to discover SMTP server: %v", domain, err)
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

	s.e.Logger.Printf("Domain %q: configured upstream SMTP server: %v", domain, u)

	c, err := s.dialSMTP(domain)
	if err != nil {
		s.e.Logger.Printf("Warning: SMTP server %v not reachable at startup: %v", u, err)
	} else {
		c.Close()
	}
	return nil
}

func (s *Server) load() error {
	// Reload runs the loaders again, and a plugin that registered a
	// callback last time is about to register it again. Start empty so
	// the list does not grow a stale entry per reload.
	s.OnAccountGone = nil
	var plugins []Plugin
	for _, load := range pluginLoaders {
		l, err := load(s)
		if err != nil {
			return fmt.Errorf("failed to load plugins: %v", err)
		}
		for _, p := range l {
			s.e.Logger.Printf("Loaded plugin %q", p.Name())
		}
		plugins = append(plugins, l...)
	}

	renderer := newRenderer(s.e.Logger, s.Options.ThemesPath, s.Options.Theme)
	if err := renderer.Load(plugins); err != nil {
		return fmt.Errorf("failed to load templates: %v", err)
	}

	// Once we've loaded plugins and templates from disk (which can take time),
	// swap them in the Server struct
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Close previous plugins
	for _, p := range s.plugins {
		if err := p.Close(); err != nil {
			s.e.Logger.Printf("Failed to unload plugin %q: %v", p.Name(), err)
		}
	}

	s.plugins = plugins
	s.e.Renderer = renderer

	for _, p := range plugins {
		p.SetRoutes(s.e.Group(""))
	}

	return nil
}

// Reload loads Lua plugins and templates from disk.
func (s *Server) Reload() error {
	s.e.Logger.Printf("Reloading server")
	return s.load()
}

// Logger returns this server's logger.
func (s *Server) Logger() echo.Logger {
	return s.e.Logger
}

// Context is the context used by HTTP handlers.
//
// Use a type assertion to get it from a echo.Context:
//
//	ctx := ectx.(*alborz.Context)
type Context struct {
	echo.Context
	Server         *Server
	Session        *Session // request-scoped account; nil if not logged in
	DefaultSession *Session // account stored in the session cookie

	// Unified marks the merged all-accounts view; Session then anchors
	// to one of the accounts while handlers aware of the view iterate
	// Sessions.
	Unified bool

	// Request-scoped account list. The accounts cookie only reflects
	// the request, so consecutive account changes in one handler must
	// read their own writes here.
	accounts       []*Session
	accountsLoaded bool

	// Per-request upstream and render durations, reported in Server-Timing.
	timing *Timing

	// Account named by the request's account parameter, selected for this
	// request only; empty otherwise.
	urlAccount string
}

var aLongTimeAgo = time.Unix(233431200, 0)

// secureCookies reports whether cookies should be marked Secure. Behind a
// TLS-terminating reverse proxy the request itself is plain HTTP, so the
// forwarded protocol decides, which Scheme consults.
func (ctx *Context) secureCookies() bool {
	return ctx.Scheme() == "https"
}

// SetSession sets a cookie for the provided session. Passing a nil session
// unsets the cookie.
func (ctx *Context) SetSession(s *Session) {
	cookie := http.Cookie{
		Name:     cookieName,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   ctx.secureCookies(),
	}
	if s != nil {
		cookie.Value = s.token
		ctx.setActiveUser(s.username)
	} else {
		cookie.Expires = aLongTimeAgo // unset the cookie
	}
	ctx.SetCookie(&cookie)
}

// setActiveUser records the active account's username so the login page can
// re-authenticate the same identity after its session expired. It
// deliberately survives session expiry; only the final logout clears it.
func (ctx *Context) setActiveUser(username string) {
	cookie := http.Cookie{
		Name:     activeUserCookieName,
		Path:     "/",
		Expires:  time.Now().Add(credentialCookieLife),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   ctx.secureCookies(),
	}
	if username == "" {
		cookie.Expires = aLongTimeAgo // unset the cookie
	} else {
		cookie.Value = url.QueryEscape(username)
	}
	ctx.SetCookie(&cookie)
}

// Account describes one signed-in account for the account switcher.
type Account struct {
	Username string
	Active   bool
}

// accountSessions returns the live sessions listed in the accounts cookie in
// order. The active session is always a member; expired entries are pruned
// and the cookie rewritten.
func (ctx *Context) accountSessions() []*Session {
	if ctx.accountsLoaded {
		return ctx.accounts
	}

	var tokens []string
	if value, ok := ctx.cookieValue(accountsCookieName, func(string) bool { return true }); ok {
		cookie := &http.Cookie{Value: value}
		tokens = strings.Split(cookie.Value, "|")
	}

	var sessions []*Session
	changed := false
	activeListed := false
	for _, token := range tokens {
		s, err := ctx.Server.Sessions.get(token)
		if err != nil {
			changed = true
			continue
		}
		if s == ctx.Session {
			activeListed = true
		}
		sessions = append(sessions, s)
	}
	if ctx.Session != nil && !activeListed {
		sessions = append([]*Session{ctx.Session}, sessions...)
		changed = true
	}
	if changed {
		ctx.setAccountSessions(sessions)
	} else {
		ctx.accounts = sessions
		ctx.accountsLoaded = true
	}
	return sessions
}

func (ctx *Context) setAccountSessions(sessions []*Session) {
	ctx.accounts = sessions
	ctx.accountsLoaded = true

	cookie := http.Cookie{
		Name:     accountsCookieName,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   ctx.secureCookies(),
	}
	if len(sessions) == 0 {
		cookie.Expires = aLongTimeAgo // unset the cookie
	} else {
		tokens := make([]string, len(sessions))
		for i, s := range sessions {
			tokens[i] = s.token
		}
		cookie.Value = strings.Join(tokens, "|")
	}
	ctx.SetCookie(&cookie)
}

// Accounts lists the signed-in accounts in switcher order.
func (ctx *Context) Accounts() []Account {
	sessions := ctx.accountSessions()
	accounts := make([]Account, len(sessions))
	for i, s := range sessions {
		accounts[i] = Account{Username: s.username, Active: s == ctx.Session}
	}
	return accounts
}

// Sessions lists the live sessions of every signed-in account, the
// active one included.
func (ctx *Context) Sessions() []*Session {
	return ctx.accountSessions()
}

// themeVariants are the selectable stylesheet overlays; the value is
// validated here since it lands in a stylesheet URL.
var themeVariants = []string{"sublime", "glass", "ink"}

// setPref stores a per-user display preference in the browser; empty
// clears it back to the default.
// cookieValues returns every value the request carries for a name.
// A browser may hold more than one cookie of the same name - an older
// deploy's, set on a narrower path, outranks ours and is sent first -
// and http.Request.Cookie only ever returns that first one. Reading one
// value made a stale cookie shadow the live one for good: the session
// looked expired, the login page restored it, the redirect came back,
// and the stale cookie shadowed it again. We cannot delete a cookie
// whose path we do not know, so we tolerate it instead.
func (ctx *Context) cookieValues(name string) []string {
	var values []string
	for _, c := range ctx.Request().Cookies() {
		if c.Name == name {
			values = append(values, c.Value)
		}
	}
	return values
}

// cookieValue returns the first value for a name that passes valid,
// so a stale duplicate cannot mask a usable one.
func (ctx *Context) cookieValue(name string, valid func(string) bool) (string, bool) {
	for _, v := range ctx.cookieValues(name) {
		if valid(v) {
			return v, true
		}
	}
	return "", false
}

func (ctx *Context) setPref(name, value string, valid bool) {
	cookie := http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   ctx.secureCookies(),
		Expires:  time.Now().Add(preferenceCookieLife),
	}
	if value == "" || !valid {
		cookie.Expires = aLongTimeAgo // unset the cookie
	}
	ctx.SetCookie(&cookie)
}

func (ctx *Context) pref(name string, valid func(string) bool) string {
	if v, ok := ctx.cookieValue(name, valid); ok {
		return v
	}
	return ""
}

// SetColorScheme stores the forced light or dark scheme per user.
func (ctx *Context) SetColorScheme(scheme string) {
	ctx.setPref(schemeCookieName, scheme, scheme == "light" || scheme == "dark")
}

// ColorScheme returns the user's forced scheme, empty for the system.
func (ctx *Context) ColorScheme() string {
	return ctx.pref(schemeCookieName, func(v string) bool { return v == "light" || v == "dark" })
}

// SetTheme stores the theme variant choice per user.
func (ctx *Context) SetTheme(theme string) {
	ctx.setPref(themeCookieName, theme, slices.Contains(themeVariants, theme))
}

// Theme returns the user's theme variant, empty for the default.
func (ctx *Context) Theme() string {
	return ctx.pref(themeCookieName, func(v string) bool { return slices.Contains(themeVariants, v) })
}

// SetAccountColors stores whether merged lists mark each row with its
// account's color. It is a reading aid of one browser, not a property
// of the accounts, so it stays out of their stores.
func (ctx *Context) SetAccountColors(on bool) {
	value := ""
	if on {
		value = "1"
	}
	ctx.setPref(accountColorsCookieName, value, on)
}

// AccountColors reports whether the color marks are switched on.
func (ctx *Context) AccountColors() bool {
	return ctx.pref(accountColorsCookieName, func(v string) bool { return v == "1" }) == "1"
}

// SetAlignByScript stores whether a line aligns by its own script
// rather than with the interface's edge.
func (ctx *Context) SetAlignByScript(on bool) {
	value := ""
	if on {
		value = "1"
	}
	ctx.setPref(alignCookieName, value, on)
}

// AlignByScript reports whether lines align by their own script.
func (ctx *Context) AlignByScript() bool {
	return ctx.pref(alignCookieName, func(v string) bool { return v == "1" }) == "1"
}

// textSizes are the reading sizes on offer beside the default. Every
// font size in the stylesheet is already relative, so one declaration
// on the root scales the whole interface.
var textSizes = []string{"large", "larger"}

// SetTextSize stores the reader's chosen text size. It is a property of
// the eyes reading the page rather than of any account, so it lives in
// the browser beside the theme and the language.
func (ctx *Context) SetTextSize(size string) {
	ctx.setPref(textSizeCookieName, size, slices.Contains(textSizes, size))
}

// TextSize returns the chosen size, empty for the default.
func (ctx *Context) TextSize() string {
	return ctx.pref(textSizeCookieName, func(v string) bool { return slices.Contains(textSizes, v) })
}

// secondaryCalendars are the calendar systems that can be shown beside
// the Gregorian one; empty shows none.
var secondaryCalendars = []string{"shcal"}

// displayKey holds the choices that follow the person rather than the
// browser, in the account's own store: which calendar someone counts in
// is not a property of the machine they read mail on.
const displayKey = "alborz.display"

type displayPrefs struct {
	Secondary string
}

// displayMemo keeps the store off the render path. The store does not
// remember a missing entry, so an unmade choice would otherwise cost a
// METADATA round trip on every page, behind the lock every IMAP command
// of the session queues on.
var displayMemo = NewMemo[displayPrefs](time.Hour)

func (ctx *Context) displayPrefs() displayPrefs {
	if ctx.Session == nil {
		return displayPrefs{}
	}
	prefs, err := displayMemo.Get(ctx.Session.Username(), func() (displayPrefs, error) {
		var p displayPrefs
		err := ctx.Session.Store().Get(displayKey, &p)
		if err == ErrNoStoreEntry {
			err = nil
		}
		return p, err
	})
	if err != nil {
		ctx.Logger().Printf("failed to read display preferences: %v", err)
	}
	return prefs
}

// SetSecondaryCalendar stores which calendar system is shown alongside
// the Gregorian dates.
func (ctx *Context) SetSecondaryCalendar(name string) error {
	if !slices.Contains(secondaryCalendars, name) {
		name = ""
	}
	prefs := ctx.displayPrefs()
	if prefs.Secondary == name {
		return nil
	}
	prefs.Secondary = name
	if err := ctx.Session.Store().Put(displayKey, &prefs); err != nil {
		return err
	}
	displayMemo.Forget(ctx.Session.Username())
	return nil
}

// SecondaryCalendar returns the chosen system, empty for none.
func (ctx *Context) SecondaryCalendar() string {
	return ctx.displayPrefs().Secondary
}

// SetLanguage stores the user's UI language choice in the browser,
// per user rather than per account; empty follows Accept-Language
// again.
func (ctx *Context) SetLanguage(code string) {
	cookie := http.Cookie{
		Name:     langCookieName,
		Value:    code,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   ctx.secureCookies(),
		Expires:  time.Now().Add(preferenceCookieLife),
	}
	if code == "" || !IsLanguage(code) {
		cookie.Expires = aLongTimeAgo // unset the cookie
	}
	ctx.SetCookie(&cookie)
}

// Language returns the user's explicit UI language choice, empty when
// following the browser preference.
func (ctx *Context) Language() string {
	if c, ok := ctx.cookieValue(langCookieName, IsLanguage); ok {
		return c
	}
	return ""
}

// SetUnified turns the merged all-accounts view on or off; it needs at
// least two signed-in accounts to turn on.
func (ctx *Context) SetUnified(on bool) {
	cookie := http.Cookie{
		Name:     unifiedCookieName,
		Value:    "1",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   ctx.secureCookies(),
	}
	if !on || len(ctx.accountSessions()) < 2 {
		cookie.Expires = aLongTimeAgo
		ctx.Unified = false
	} else {
		ctx.Unified = true
	}
	ctx.SetCookie(&cookie)
}

// AddAccount makes s the active session and appends it to the account list.
// A previous session for the same username is closed and replaced in place.
func (ctx *Context) AddAccount(s *Session) {
	sessions := ctx.accountSessions()
	replaced := false
	for i, other := range sessions {
		if other.username == s.username {
			other.Close()
			sessions[i] = s
			replaced = true
		}
	}
	if !replaced {
		sessions = append(sessions, s)
	}
	ctx.Session = s
	ctx.DefaultSession = s
	ctx.SetSession(s)
	ctx.setAccountSessions(sessions)
}

// URLAccount returns the account selected by the request's account
// parameter, "" when none.
func (ctx *Context) URLAccount() string {
	return ctx.urlAccount
}

// AddressParam writes an address into a URL query the way a reader
// would type it. RFC 3986 allows "@" unescaped in a query, and every
// address in alborz's links is one, so escaping it only makes the bar
// unreadable. Everything else is escaped as usual.
func AddressParam(address string) string {
	return strings.ReplaceAll(url.QueryEscape(address), "%40", "@")
}

// AddressQuery encodes a whole query the way AddressParam encodes one
// value: an address in it keeps its at sign, for the same reason.
func AddressQuery(q url.Values) string {
	return strings.ReplaceAll(q.Encode(), "%40", "@")
}

// AccountPath appends the request's account parameter to path, so a flow
// started under one account redirects back into it.
func (ctx *Context) AccountPath(path string) string {
	if ctx.urlAccount == "" {
		return path
	}
	return path + "?account=" + AddressParam(ctx.urlAccount)
}

// NextOr returns the local page a form asked to return to, or fallback
// when the request names none, names another host, or names something
// that is not an absolute path. A setting written from a page belongs
// to that page: the form carries where it was submitted from, and the
// handler comes back to it instead of a fixed landing page.
func (ctx *Context) NextOr(fallback string) string {
	next := ctx.FormValue("next")
	if next == "" {
		return fallback
	}
	// A backslash is read as a slash by browsers, so "/\\host" would
	// leave the site the way "//host" does.
	u, err := url.Parse(next)
	if err != nil || u.Host != "" || !strings.HasPrefix(u.Path, "/") ||
		strings.ContainsRune(next, '\\') {
		return fallback
	}
	return u.String()
}

// SessionFor returns the listed account's live session without making it
// active, nil when the account is not signed in.
func (ctx *Context) SessionFor(username string) *Session {
	for _, s := range ctx.accountSessions() {
		if s.username == username {
			return s
		}
	}
	return nil
}

// SwitchAccount makes the listed account with the given username active. It
// reports false when no live session for that username remains.
func (ctx *Context) SwitchAccount(username string) bool {
	s := ctx.SessionFor(username)
	if s == nil {
		return false
	}
	ctx.Session = s
	ctx.DefaultSession = s
	ctx.SetSession(s)
	return true
}

// Logout closes the active session, removes it from the account list, and
// promotes the next remaining account, whose session is returned. A nil
// return means no account is left and the cookies were cleared.
func (ctx *Context) Logout() *Session {
	return ctx.LogoutAccount(ctx.Session.username)
}

// LogoutAccount closes exactly the named account. Logging out a background
// account preserves the current session; logging out the current account
// promotes the first remaining one.
func (ctx *Context) LogoutAccount(username string) *Session {
	var remaining []*Session
	var target *Session
	for _, s := range ctx.accountSessions() {
		if s.username == username {
			target = s
			continue
		}
		remaining = append(remaining, s)
	}
	if target == nil {
		return ctx.DefaultSession
	}
	target.Close()
	ctx.setAccountSessions(remaining)
	ctx.forgetLoginToken(target.username)
	if len(remaining) == 0 {
		ctx.Session = nil
		ctx.DefaultSession = nil
		ctx.SetSession(nil)
		ctx.setActiveUser("")
		return nil
	}
	if ctx.DefaultSession == target || ctx.DefaultSession == nil {
		ctx.DefaultSession = remaining[0]
	}
	ctx.Session = ctx.DefaultSession
	ctx.SetSession(ctx.DefaultSession)
	return ctx.DefaultSession
}

type loginToken struct {
	Username string
	Password string
}

// SetLoginToken remembers the account's credentials for automatic
// re-authentication after session expiry. Re-login of a known account
// updates its entry in place. The cookie spans the site so logging
// out at /logout can forget the account's credentials; the payload
// stays fernet-encrypted with the server's login key.
func (ctx *Context) SetLoginToken(username, password string) {
	tokens := ctx.loginTokens()
	updated := false
	for i := range tokens {
		if tokens[i].Username == username {
			tokens[i].Password = password
			updated = true
		}
	}
	if !updated {
		tokens = append(tokens, loginToken{username, password})
	}
	ctx.storeLoginTokens(tokens)
}

// forgetLoginToken drops the account's remembered credentials, so logging
// out also forgets how to log back in.
func (ctx *Context) forgetLoginToken(username string) {
	tokens := ctx.loginTokens()
	kept := make([]loginToken, 0, len(tokens))
	for _, t := range tokens {
		if t.Username != username {
			kept = append(kept, t)
		}
	}
	if len(kept) < len(tokens) {
		ctx.storeLoginTokens(kept)
	}
}

func (ctx *Context) storeLoginTokens(tokens []loginToken) {
	cookie := http.Cookie{
		Expires:  time.Now().Add(credentialCookieLife),
		Name:     loginTokenCookieName,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   ctx.secureCookies(),
		Path:     "/",
	}
	if len(tokens) == 0 {
		cookie.Expires = aLongTimeAgo // unset the cookie
		ctx.SetCookie(&cookie)
		return
	}

	payload, err := json.Marshal(tokens)
	if err != nil {
		panic(err) // Should never happen
	}
	fkey := ctx.Server.Options.LoginKey
	if fkey == nil {
		return
	}

	bytes, err := fernet.EncryptAndSign(payload, fkey)
	if err != nil {
		log.Printf("Warning: login token encryption failed: %v", err)
		return
	}

	cookie.Value = string(bytes)
	ctx.SetCookie(&cookie)
}

func (ctx *Context) loginTokens() []loginToken {
	cookie, err := ctx.Cookie(loginTokenCookieName)
	if err != nil || cookie == nil {
		return nil
	}

	fkey := ctx.Server.Options.LoginKey
	if fkey == nil {
		return nil
	}

	bytes := fernet.VerifyAndDecrypt([]byte(cookie.Value),
		credentialCookieLife, []*fernet.Key{fkey})
	if bytes == nil {
		return nil
	}

	var tokens []loginToken
	if err := json.Unmarshal(bytes, &tokens); err != nil {
		// A cookie from the single-account format; forget it.
		return nil
	}
	return tokens
}

// RestoreRememberedAccounts signs every remembered account back in,
// leaving the recorded active one active, and reports whether any
// account came back.
const (
	// loginRetryAfter is how long a failed automatic sign-in is left
	// alone. Without it every request retries every remembered account,
	// so one unreachable server turns a browser reload - or a page's own
	// assets - into a burst of logins, which is what a provider counting
	// them refuses the account for.
	loginRetryAfter = 2 * time.Minute
)

var loginFailures sync.Map // username -> time.Time of the last failure

// recentlyFailed reports whether signing this account in was tried and
// failed too recently to be worth trying again.
func recentlyFailed(username string) bool {
	when, ok := loginFailures.Load(username)
	if !ok {
		return false
	}
	return time.Since(when.(time.Time)) < loginRetryAfter
}

func (ctx *Context) RestoreRememberedAccounts() bool {
	// The accounts sign in concurrently: one unreachable upstream must
	// not add its timeout to the others' wait.
	tokens := ctx.loginTokens()
	sessions := make([]*Session, len(tokens))
	var wg sync.WaitGroup
	for i, token := range tokens {
		if recentlyFailed(token.Username) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := ctx.Server.Sessions.Put(token.Username, token.Password)
			if err != nil {
				loginFailures.Store(token.Username, time.Now())
				ctx.Logger().Printf("Login failed for %q: %v", token.Username, err)
				return
			}
			loginFailures.Delete(token.Username)
			sessions[i] = s
		}()
	}
	wg.Wait()

	restored := false
	for _, s := range sessions {
		if s == nil {
			continue
		}
		ctx.AddAccount(s)
		if ctx.Server.OnAccountReady != nil {
			ctx.Server.OnAccountReady(s, ctx.Logger())
		}
		restored = true
	}
	if !restored {
		return false
	}
	if cookie, err := ctx.Cookie(activeUserCookieName); err == nil && cookie != nil {
		if active, err := url.QueryUnescape(cookie.Value); err == nil {
			ctx.SwitchAccount(active)
		}
	}
	return true
}

// assetURL stamps a theme asset's URL with a digest of its content so the
// browser can keep it instead of revalidating it on every page; a changed
// file changes its URL. Disk overrides win over the embedded copy, exactly
// as serving does.
func (s *Server) assetURL(name string) string {
	if stamp := s.assetStamp(name); stamp != "" {
		return "/assets/" + name + "?v=" + stamp
	}
	return "/assets/" + name
}

// assetStamp returns the asset's content digest. Digests are cached: an
// embedded asset cannot change while the process runs, and a disk override
// is re-read when its modification time or size changes, which is what
// keeps local CSS edits showing up on browser reload.
func (s *Server) assetStamp(name string) string {
	key := "embedded"
	diskPath := filepath.Join(s.Options.ThemesPath, s.Options.Theme, "assets", name)
	onDisk := false
	if info, err := os.Stat(diskPath); err == nil {
		onDisk = true
		key = fmt.Sprintf("%d.%d", info.ModTime().UnixNano(), info.Size())
	}

	s.assetsMu.Lock()
	defer s.assetsMu.Unlock()
	if e, ok := s.assets[name]; ok && e.key == key {
		return e.stamp
	}

	var content []byte
	var err error
	if onDisk {
		content, err = os.ReadFile(diskPath)
	} else {
		content, err = fs.ReadFile(embeddedTheme, path.Join("themes/alborz/assets", name))
	}
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(content)
	stamp := hex.EncodeToString(sum[:4])
	s.assets[name] = assetStamp{key: key, stamp: stamp}
	return stamp
}

func isPublic(path string) bool {
	if strings.HasPrefix(path, "/plugins/") {
		parts := strings.Split(path, "/")
		return len(parts) >= 4 && parts[3] == "assets"
	}
	// The manifest names the application, not anything in it, and a
	// browser fetches it before anyone has signed in.
	return path == "/login" || path == "/manifest.webmanifest" ||
		strings.HasPrefix(path, "/assets/")
}

func redirectToLogin(ctx *Context) error {
	path := ctx.Request().URL.Path
	to := "/login"
	// Only a GET can be resumed after login; an intercepted POST must
	// not become a GET to its own, often POST-only, route.
	if ctx.Request().Method == http.MethodGet && path != "/" && path != "/login" {
		to += "?next=" + url.QueryEscape(ctx.Request().URL.String())
	}
	return ctx.Redirect(http.StatusFound, to)
}

// renderUpstreamDown answers for a server that did not reply. It says
// which one and how long was waited, because a reader who is told
// "internal error" checks their password, and the password is not the
// problem. Trying again is offered because it is the thing that works:
// a timeout is usually a moment rather than a state.
func renderUpstreamDown(ctx *Context, cause UpstreamError) error {
	type upstreamRenderData struct {
		BaseRenderData
		Service string
		Seconds string
		// Waited separates "asked and got no answer" from "could not
		// get a connection at all"; only the first has a number.
		Waited bool
		Retry  string
	}
	data := upstreamRenderData{
		BaseRenderData: *NewBaseRenderData(ctx),
		Service:        cause.Service,
		Seconds:        fmt.Sprintf("%d", int(cause.After.Seconds())),
		Waited:         cause.After > 0,
		Retry:          ctx.Request().URL.RequestURI(),
	}
	data.BaseRenderData.WithTitle(ctx.T("upstream.title"))
	return ctx.Render(http.StatusBadGateway, "upstream.html", &data)
}

func handleUnauthenticated(next echo.HandlerFunc, ctx *Context) error {
	// Require auth for all requests except /login and assets
	if isPublic(ctx.Request().URL.Path) {
		return next(ctx)
	} else {
		return redirectToLogin(ctx)
	}
}

// ForgetAccount tells every plugin that asked that an account has
// signed out. It is called after the session is closed, so a plugin can
// drop what it was holding rather than keep it warm for nobody.
func (s *Server) ForgetAccount(username string) {
	for _, forget := range s.OnAccountGone {
		forget(username)
	}
}

// OnAccountReady runs when an account is signed in, including one
// restored from remembered credentials. The base plugin uses it to start
// following the mailbox; the root package does not know what following
// means, so it only makes the call.
type AccountReadyFunc func(*Session, echo.Logger)

type Options struct {
	Upstreams  []string
	Theme      string
	ThemesPath string
	Debug      bool
	LoginKey   *fernet.Key
	Version    string
	// ProjectURL is where the footer's name links, for a deployment that
	// wants to point somewhere. Empty by default: a deployment is not the
	// author's, and no address of anyone's belongs in a shipped binary.
	ProjectURL string
}

// New creates a new server.
func New(e *echo.Echo, options *Options) (*Server, error) {
	s, err := newServer(e, options)
	if err != nil {
		return nil, err
	}

	if err := s.load(); err != nil {
		return nil, err
	}

	e.HTTPErrorHandler = func(err error, ctx echo.Context) {
		code := http.StatusInternalServerError
		var he *echo.HTTPError
		if errors.As(err, &he) {
			code = he.Code
		} else if errors.Is(err, ErrNotFound) {
			code = http.StatusNotFound
		}

		// Not-found answers name what is missing instead of showing
		// the generic error page.
		if code == http.StatusNotFound {
			message := err.Error()
			if he != nil {
				if m, ok := he.Message.(string); ok {
					message = m
				}
			}
			if actx, ok := ctx.Get("context").(*Context); ok {
				if err := RenderInfo(actx, code, message); err == nil {
					return
				}
			}
		}

		// A server that did not answer is not this program failing, and a
		// 500 says it is. It gets a page of its own that names what did
		// not answer and offers the one thing that helps.
		var upstream UpstreamError
		if errors.As(err, &upstream) {
			if actx, ok := ctx.Get("context").(*Context); ok {
				if err := renderUpstreamDown(actx, upstream); err == nil {
					return
				}
			}
		}

		type ErrorRenderData struct {
			BaseRenderData
			Code   int
			Err    error
			Status string
		}
		rdata := ErrorRenderData{
			BaseRenderData: *NewBaseRenderData(ctx),
			Err:            err,
			Code:           code,
			Status:         http.StatusText(code),
		}

		if err := ctx.Render(code, "error.html", &rdata); err != nil {
			ctx.Logger().Error(fmt.Errorf(
				"Error occured rendering error page: %w. How meta.", err))
		}

		ctx.Logger().Error(err)
	}

	e.Pre(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ectx echo.Context) error {
			s.mutex.RLock()
			err := next(ectx)
			s.mutex.RUnlock()
			return err
		}
	})

	// HTML and stylesheets shrink several-fold over slow links; binary
	// assets and raw message parts are left alone.
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Skipper: func(c echo.Context) bool {
			p := c.Request().URL.Path
			return strings.HasSuffix(p, ".png") || strings.HasSuffix(p, "/raw")
		},
		MinLength: 1024,
	}))

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ectx echo.Context) error {
			// `style-src 'unsafe-inline'` is required for e-mails with
			// embedded stylesheets
			ectx.Response().Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
			// DNS prefetching has privacy implications
			ectx.Response().Header().Set("X-DNS-Prefetch-Control", "off")
			// Assets revalidate by default so theme and plugin edits are
			// picked up; the theme asset handler upgrades URLs stamped
			// with the current content to immutable.
			path := ectx.Request().URL.Path
			if strings.HasPrefix(path, "/assets/") || strings.HasPrefix(path, "/plugins/") {
				ectx.Response().Header().Set("Cache-Control", "no-cache")
			}
			return next(ectx)
		}
	})

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ectx echo.Context) error {
			ctx := &Context{Context: ectx, Server: s}
			ctx.Set("context", ctx)
			ctx.installTiming()

			var session *Session
			for _, value := range ctx.cookieValues(cookieName) {
				s, err := ctx.Server.Sessions.get(value)
				if err == nil {
					session = s
					break
				}
				if err != ErrSessionExpired {
					return err
				}
			}

			// A session lives 30 minutes past the last request; the
			// remembered credentials live 30 days. Sending the reader to
			// the login page while the browser holds a valid token reads
			// as being logged out, and an intercepted POST - a message
			// being sent - was lost outright, because only a GET can be
			// resumed. Sign the accounts back in and carry on with the
			// request that arrived.
			if session == nil && !isPublic(ctx.Request().URL.Path) {
				if ctx.RestoreRememberedAccounts() {
					session = ctx.Session
				}
			}
			if session == nil {
				ctx.SetSession(nil)
				return handleUnauthenticated(next, ctx)
			}
			ctx.Session = session
			ctx.DefaultSession = ctx.Session
			// Every signed-in account is in use, not only the active
			// one: the switcher lists them all, so they all stay alive.
			for _, session := range ctx.accountSessions() {
				session.ping()
			}

			// A link may carry the account it belongs to; it selects the
			// session for this request only, so following it changes no
			// state and the unified view survives the round trip.
			if acct := ctx.QueryParam("account"); acct != "" {
				session := ctx.SessionFor(acct)
				if session == nil {
					return RenderInfo(ctx, http.StatusOK, fmt.Sprintf(ctx.T("account.notsignedin"), acct))
				}
				ctx.Session = session
				ctx.urlAccount = acct
			} else if strings.HasPrefix(ctx.Request().URL.Path, "/mailbox/") &&
				ctx.QueryParam("all") == "1" && len(ctx.accountSessions()) > 1 {
				// The merged view as a place, not a mode: nothing sticks.
				ctx.Unified = true
			} else if _, err := ctx.Cookie(unifiedCookieName); strings.HasPrefix(ctx.Request().URL.Path, "/mailbox/") &&
				err == nil && len(ctx.accountSessions()) > 1 {
				ctx.Unified = true
			}

			return next(ctx)
		}
	})

	// The manifest is generated rather than a file, so the name a phone
	// puts under the icon is BrandName and not a second copy of it that
	// can drift. minimal-ui rather than standalone: sessions live in
	// memory, and standalone takes the address bar away, so a restart
	// would land on /login in a window with no way out.
	e.GET("/manifest.webmanifest", func(ectx echo.Context) error {
		icon := func(name, purpose string) map[string]string {
			out := map[string]string{"src": s.assetURL(name), "sizes": "512x512", "type": "image/png"}
			if strings.HasSuffix(name, "192.png") {
				out["sizes"] = "192x192"
			}
			if purpose != "" {
				out["purpose"] = purpose
			}
			return out
		}
		ectx.Response().Header().Set("Content-Type", "application/manifest+json")
		return ectx.JSON(http.StatusOK, map[string]any{
			"name":             BrandName,
			"short_name":       BrandName,
			"start_url":        "/",
			"scope":            "/",
			"display":          "minimal-ui",
			"background_color": brandBackground,
			"theme_color":      brandColor,
			"icons": []map[string]string{
				icon("icon-192.png", ""),
				icon("icon-512.png", ""),
				icon("icon-maskable-512.png", "maskable"),
			},
		})
	})

	// Assets are served from the embedded theme, with the theme directory
	// on disk taking precedence file by file, like templates.
	embeddedAssets, err := fs.Sub(embeddedTheme, "themes/alborz/assets")
	if err != nil {
		return nil, err
	}
	e.GET("/assets/*", func(ectx echo.Context) error {
		name := strings.TrimPrefix(path.Clean("/"+ectx.Param("*")), "/")
		if name == "" {
			return echo.ErrNotFound
		}
		// The immutable flag is earned by naming the current content; a
		// stale version string keeps the no-cache default.
		if v := ectx.QueryParam("v"); v != "" && v == s.assetStamp(name) {
			ectx.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		diskPath := filepath.Join(options.ThemesPath, options.Theme, "assets", name)
		if _, err := os.Stat(diskPath); err == nil {
			return ectx.File(diskPath)
		}
		http.ServeFileFS(ectx.Response(), ectx.Request(), embeddedAssets, name)
		return nil
	})

	return s, nil
}
