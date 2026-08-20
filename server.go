package alborz

import (
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
)

// brandName suffixes every page title.
const brandName = "Alborz"

const (
	cookieName           = "alborz_session"
	accountsCookieName   = "alborz_accounts"
	activeUserCookieName = "alborz_user"
	loginTokenCookieName = "alborz_login_tokens"
	unifiedCookieName    = "alborz_unified"
	schemeCookieName     = "alborz_scheme"
	themeCookieName      = "alborz_theme"
)

// Server holds all the alborz server state.
type Server struct {
	e        *echo.Echo
	Sessions *SessionManager
	Options  *Options

	mutex   sync.RWMutex // used for server reload
	plugins []Plugin

	domains map[string]*domainUpstreams
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
	s := &Server{e: e, Options: options}

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

	s.Sessions = newSessionManager(s.dialIMAP, s.dialSMTP, s.dialSieve, e.Logger)
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
// auto-discovery with URL.Host.
func (s *Server) Upstream(domain string, schemes ...string) (*url.URL, error) {
	d, ok := s.upstreamsFor(domain)
	if !ok {
		return nil, UnknownDomainError{domain}
	}
	var urls []*url.URL
	for _, scheme := range append(schemes, "") {
		u, ok := d.upstreams[scheme]
		if ok {
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
	Server  *Server
	Session *Session // nil if user isn't logged in

	// Unified marks the merged all-accounts view; Session then anchors
	// to one of the accounts while handlers aware of the view iterate
	// Sessions.
	Unified bool

	// Request-scoped account list. The accounts cookie only reflects
	// the request, so consecutive account changes in one handler must
	// read their own writes here.
	accounts       []*Session
	accountsLoaded bool
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
		Expires:  time.Now().Add(30 * 24 * time.Hour),
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
	if cookie, err := ctx.Cookie(accountsCookieName); err == nil {
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
var themeVariants = []string{"sublime", "sourcehut", "glass", "ink"}

// setPref stores a per-user display preference in the browser; empty
// clears it back to the default.
func (ctx *Context) setPref(name, value string, valid bool) {
	cookie := http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   ctx.secureCookies(),
		Expires:  time.Now().Add(365 * 24 * time.Hour),
	}
	if value == "" || !valid {
		cookie.Expires = aLongTimeAgo // unset the cookie
	}
	ctx.SetCookie(&cookie)
}

func (ctx *Context) pref(name string, valid func(string) bool) string {
	if c, err := ctx.Cookie(name); err == nil && valid(c.Value) {
		return c.Value
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
		Expires:  time.Now().Add(365 * 24 * time.Hour),
	}
	if code == "" || !IsLanguage(code) {
		cookie.Expires = aLongTimeAgo // unset the cookie
	}
	ctx.SetCookie(&cookie)
}

// Language returns the user's explicit UI language choice, empty when
// following the browser preference.
func (ctx *Context) Language() string {
	if c, err := ctx.Cookie(langCookieName); err == nil && IsLanguage(c.Value) {
		return c.Value
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
	ctx.SetSession(s)
	ctx.setAccountSessions(sessions)
}

// SwitchAccount makes the listed account with the given username active. It
// reports false when no live session for that username remains.
func (ctx *Context) SwitchAccount(username string) bool {
	for _, s := range ctx.accountSessions() {
		if s.username == username {
			ctx.Session = s
			ctx.SetSession(s)
			return true
		}
	}
	return false
}

// Logout closes the active session, removes it from the account list, and
// promotes the next remaining account, whose session is returned. A nil
// return means no account is left and the cookies were cleared.
func (ctx *Context) Logout() *Session {
	var remaining []*Session
	for _, s := range ctx.accountSessions() {
		if s != ctx.Session {
			remaining = append(remaining, s)
		}
	}
	ctx.Session.Close()
	ctx.setAccountSessions(remaining)
	ctx.forgetLoginToken(ctx.Session.username)
	if len(remaining) == 0 {
		ctx.Session = nil
		ctx.SetSession(nil)
		ctx.setActiveUser("")
		return nil
	}
	ctx.Session = remaining[0]
	ctx.SetSession(ctx.Session)
	return ctx.Session
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
		Expires:  time.Now().Add(30 * 24 * time.Hour),
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
		24*time.Hour*30, []*fernet.Key{fkey})
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
func (ctx *Context) RestoreRememberedAccounts() bool {
	restored := false
	for _, token := range ctx.loginTokens() {
		s, err := ctx.Server.Sessions.Put(token.Username, token.Password)
		if err != nil {
			ctx.Logger().Printf("Login failed for %q: %v", token.Username, err)
			continue
		}
		ctx.AddAccount(s)
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

func isPublic(path string) bool {
	if strings.HasPrefix(path, "/plugins/") {
		parts := strings.Split(path, "/")
		return len(parts) >= 4 && parts[3] == "assets"
	}
	return path == "/login" || strings.HasPrefix(path, "/assets/")
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

func handleUnauthenticated(next echo.HandlerFunc, ctx *Context) error {
	// Require auth for all requests except /login and assets
	if isPublic(ctx.Request().URL.Path) {
		return next(ctx)
	} else {
		return redirectToLogin(ctx)
	}
}

type Options struct {
	Upstreams  []string
	Theme      string
	ThemesPath string
	Debug      bool
	LoginKey   *fernet.Key
	Version    string
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

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ectx echo.Context) error {
			// `style-src 'unsafe-inline'` is required for e-mails with
			// embedded stylesheets
			ectx.Response().Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
			// DNS prefetching has privacy implications
			ectx.Response().Header().Set("X-DNS-Prefetch-Control", "off")
			// Asset URLs are not versioned; make browsers revalidate so
			// theme and plugin asset changes are picked up
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

			cookie, err := ctx.Cookie(cookieName)
			if err == http.ErrNoCookie {
				return handleUnauthenticated(next, ctx)
			} else if err != nil {
				return err
			}

			ctx.Session, err = ctx.Server.Sessions.get(cookie.Value)
			if err == ErrSessionExpired {
				ctx.SetSession(nil)
				return handleUnauthenticated(next, ctx)
			} else if err != nil {
				return err
			}
			// Every signed-in account is in use, not only the active
			// one: the switcher lists them all, so they all stay alive.
			for _, session := range ctx.accountSessions() {
				session.ping()
			}

			// A row of the unified view carries its account; following it
			// switches the whole context there.
			if acct := ctx.QueryParam("account"); acct != "" && ctx.SwitchAccount(acct) {
				ctx.SetUnified(false)
			} else if _, err := ctx.Cookie(unifiedCookieName); err == nil && len(ctx.accountSessions()) > 1 {
				ctx.Unified = true
			}

			return next(ctx)
		}
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
		diskPath := filepath.Join(options.ThemesPath, options.Theme, "assets", name)
		if _, err := os.Stat(diskPath); err == nil {
			return ectx.File(diskPath)
		}
		http.ServeFileFS(ectx.Response(), ectx.Request(), embeddedAssets, name)
		return nil
	})

	return s, nil
}
