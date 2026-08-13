package alps

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
)

const (
	cookieName           = "alps_session"
	accountsCookieName   = "alps_accounts"
	loginTokenCookieName = "alps_login_token"
)

// Server holds all the alps server state.
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
//	ctx := ectx.(*alps.Context)
type Context struct {
	echo.Context
	Server  *Server
	Session *Session // nil if user isn't logged in

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
	} else {
		cookie.Expires = aLongTimeAgo // unset the cookie
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
// return means no account is left and both cookies were cleared.
func (ctx *Context) Logout() *Session {
	var remaining []*Session
	for _, s := range ctx.accountSessions() {
		if s != ctx.Session {
			remaining = append(remaining, s)
		}
	}
	ctx.Session.Close()
	ctx.setAccountSessions(remaining)
	if len(remaining) == 0 {
		ctx.Session = nil
		ctx.SetSession(nil)
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

func (ctx *Context) SetLoginToken(username, password string) {
	cookie := http.Cookie{
		Expires:  time.Now().Add(30 * 24 * time.Hour),
		Name:     loginTokenCookieName,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   ctx.secureCookies(),
		Path:     "/login",
	}
	if username == "" {
		cookie.Expires = aLongTimeAgo // unset the cookie
		ctx.SetCookie(&cookie)
		return
	}

	loginToken := loginToken{username, password}
	payload, err := json.Marshal(loginToken)
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

func (ctx *Context) GetLoginToken() (string, string) {
	cookie, err := ctx.Cookie(loginTokenCookieName)
	if err != nil || cookie == nil {
		return "", ""
	}

	fkey := ctx.Server.Options.LoginKey
	if fkey == nil {
		return "", ""
	}

	bytes := fernet.VerifyAndDecrypt([]byte(cookie.Value),
		24*time.Hour*30, []*fernet.Key{fkey})
	if bytes == nil {
		return "", ""
	}

	var token loginToken
	err = json.Unmarshal(bytes, &token)
	if err != nil {
		panic(err) // Should never happen
	}

	return token.Username, token.Password
}

func isPublic(path string) bool {
	if strings.HasPrefix(path, "/plugins/") {
		parts := strings.Split(path, "/")
		return len(parts) >= 4 && parts[3] == "assets"
	}
	return path == "/login" || strings.HasPrefix(path, "/themes/")
}

func redirectToLogin(ctx *Context) error {
	path := ctx.Request().URL.Path
	to := "/login"
	if path != "/" && path != "/login" {
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
		if he, ok := err.(*echo.HTTPError); ok {
			code = he.Code
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
			if strings.HasPrefix(path, "/themes/") || strings.HasPrefix(path, "/plugins/") {
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

			return next(ctx)
		}
	})

	e.Static("/themes", options.ThemesPath)

	return s, nil
}
