package alborz

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz/internal/kv"
	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// BrandName is the name a person reads.
const BrandName = "Alborz"

// AppName is the name the machine reads, such as the data and cache directory.
const AppName = "alborz"

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

// What a launcher paints before any stylesheet has loaded.
const (
	brandColor      = "#ffffff"
	brandBackground = "#ffffff"
)

const (
	themeCookieName    = "alborz_theme"
	alignCookieName    = "alborz_align"
	textSizeCookieName = "alborz_text"
	// TimezoneCookieName is written by the page: only the browser knows its zone.
	TimezoneCookieName = "alborz_tz"
)

const (
	credentialCookieLife = 30 * 24 * time.Hour
)

// CredentialCookieLife is how long a remembered account is kept.
func CredentialCookieLife() time.Duration { return credentialCookieLife }

const (
	preferenceCookieLife = 365 * 24 * time.Hour
)

// Server holds all the alborz server state.
type Server struct {
	e       *echo.Echo
	Logins  *LoginManager
	Options *Options

	mutex sync.RWMutex
	funcs template.FuncMap

	domains map[string]*domainUpstreams

	// OnAccountReady is called when an account signs in.
	OnAccountReady []AccountReadyFunc

	// OnAccountGone is called when an account signs out.
	OnAccountGone []func(username string)

	// OnMessage and OnServers add another feature's part to mail's message and server pages, whose data is mail's.
	OnMessage []func(*Context, RenderData) error
	OnServers []func(*Context, RenderData) error

	// Suggest offers addresses to write to, beside mail's own correspondents.
	Suggest []func(*Context) []string

	// OnClose releases what a feature holds open.
	OnClose []func() error

	assetsMu sync.Mutex
	assets   map[string]assetStamp

	// Changes carries what the watchers saw to the open streams.
	Changes *Changes

	// Outgoing is how every request to another server leaves.
	Outgoing Outgoing

	// Sessions are the browsers signed in.
	Sessions *Sessions

	signInRefused *refusals
	data          kv.DB
	// dataPath is where data lives, for its messages.
	dataPath string

	loginFailures sync.Map

	themes []Theme

	// custom is loaded last so a deployment can override the theme without forking it.
	custom bool
}

func (s *Server) hasTheme(name string) bool {
	for _, t := range s.themes {
		if t.Name == name {
			return true
		}
	}
	return false
}

const customCSS = "custom.css"

type assetStamp struct {
	key   string
	stamp string
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
}

func newServer(e *echo.Echo, options *Options) (*Server, error) {
	s := &Server{e: e, Options: options, assets: make(map[string]assetStamp),
		Changes: newChanges(), Outgoing: NewOutgoing(), signInRefused: newRefusals(signInTries, signInWindow)}
	e.IPExtractor = echo.ExtractIPDirect()
	if len(options.TrustedProxies) > 0 {
		// echo trusts every private address by default, and anyone on such a network could write the header.
		trust := []echo.TrustOption{echo.TrustLoopback(false), echo.TrustLinkLocal(false), echo.TrustPrivateNet(false)}
		for _, n := range options.TrustedProxies {
			trust = append(trust, echo.TrustIPRange(n))
		}
		e.IPExtractor = echo.ExtractIPFromXFFHeader(trust...)
	}
	s.themes = readThemes(options.ThemesPath, options.Theme)
	_, err := os.Stat(filepath.Join(options.ThemesPath, options.Theme, "assets", customCSS))
	s.custom = err == nil

	// Absolute, so messages name a path without guessing the working directory; empty is the working directory.
	dataDir, err := filepath.Abs(options.DataDir)
	if err != nil {
		return nil, err
	}
	version, _ := Build()
	s.dataPath = filepath.Join(dataDir, storeFile)
	if s.data, err = openData(dataDir, version, e.Logger.Printf); err != nil {
		return nil, err
	}
	if err := CheckKey(s.data, s.dataPath, options.LoginKey, sessionsOwner+"/", settingsOwner+"/"); err != nil {
		return nil, err
	}
	s.Sessions = newSessions(OpenSessionRecords(s.data, options.LoginKey))
	if err := s.Sessions.Sweep(time.Now().Add(-credentialCookieLife)); err != nil {
		return nil, err
	}
	settings := openSettingsRecords(s.data, options.LoginKey)

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

	s.Logins = newLoginManager(s.dialIMAP, s.dialIMAPWatch, s.dialSMTP, s.dialSieve, e.Logger, settings)
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

type notFoundError struct {
	key  string
	args []interface{}
}

func (e *notFoundError) Error() string { return fmt.Sprintf("%s %v", e.key, e.args) }
func (e *notFoundError) Unwrap() error { return ErrNotFound }

// NotFound builds a not-found error naming the missing subject by its locale key.
func NotFound(key string, args ...interface{}) error {
	return &notFoundError{key, args}
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
	s.Logins.Close()
	s.data.Close()
	for _, release := range s.OnClose {
		if err := release(); err != nil {
			s.e.Logger.Printf("close: %v", err)
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

	s.e.Logger.Printf("Domain %q: configured upstream IMAP server: %v", domain, u)

	c, err := s.dialIMAP(domain, "")
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
		u, d.smtp.found, err = discoverSMTP(u.Host)
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
	renderer := newRenderer(s.e.Logger, s.Options.ThemesPath, s.Options.Theme)
	if err := renderer.Load(s.funcs); err != nil {
		return fmt.Errorf("failed to load templates: %v", err)
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.e.Renderer = renderer
	return nil
}

// Reload reads the templates from disk again.
func (s *Server) Reload() error {
	s.e.Logger.Printf("Reloading templates")
	return s.load()
}

// HandlerFunc serves a request.
type HandlerFunc func(*Context) error

// Router adds a feature's routes to the server.
type Router struct{ e *echo.Echo }

func (s *Server) Router() Router { return Router{s.e} }

// Pre answers requests ahead of routing, sessions and cookies, inside the reload lock.
func (s *Server) Pre(mw echo.MiddlewareFunc) { s.e.Pre(mw) }

func (r Router) GET(path string, h HandlerFunc)  { r.add(http.MethodGet, path, h) }
func (r Router) POST(path string, h HandlerFunc) { r.add(http.MethodPost, path, h) }

func (r Router) add(method, path string, h HandlerFunc) {
	r.e.Add(method, path, func(ectx echo.Context) error { return h(ectx.(*Context)) })
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
	Server *Server
	// quiet marks an action whose result shows in place, so it writes no notice.
	quiet         bool
	session       *Session
	sessionSecret string
	Login         *Login
	DefaultLogin  *Login // the first listed account, for a page that needs one and was given none

	// Unified marks the merged all-accounts view; Login then anchors to one of its accounts.
	Unified bool

	// The accounts cookie only reflects the request, so account changes within one handler read their own writes here.
	accounts       []*Login
	accountsLoaded bool
	lostAccounts   []string

	// A cookie written to the answer is not in the request, so a handler reads its own writes here.
	prefs map[string]string

	timing *Timing

	urlAccount string
}

var aLongTimeAgo = time.Unix(233431200, 0)

// Relocate makes the page load path whole instead of the piece it asked for.
func (ctx *Context) Relocate(path string) error {
	ctx.Response().Header().Set("HX-Redirect", path)
	return ctx.NoContent(http.StatusOK)
}

// Origin is where alborz is reached from outside: -public-url, else this request's address.
func (ctx *Context) Origin() string {
	if u := ctx.Server.Options.PublicURL; u != nil {
		return u.Scheme + "://" + u.Host
	}
	return ctx.Scheme() + "://" + ctx.Request().Host
}

// Behind a TLS-terminating proxy the request is plain HTTP; Scheme consults the forwarded protocol.
func (ctx *Context) secureCookies() bool {
	return ctx.Scheme() == "https"
}

func (ctx *Context) cookie(name, value string, life time.Duration) *http.Cookie {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		// Lax, not Strict: Strict withholds the cookie from an installed PWA's cold launch, which then opens signed out.
		SameSite: http.SameSiteLaxMode,
		Secure:   ctx.secureCookies(),
	}
	switch {
	case value == "":
		c.Expires = aLongTimeAgo
	case life > 0:
		c.Expires = time.Now().Add(life)
	}
	return c
}

// AssetURL stamps a theme asset's URL with a digest of its content, so a changed file changes its URL.
func (s *Server) AssetURL(name string) string {
	if stamp := s.assetStamp(name); stamp != "" {
		return "/assets/" + name + "?v=" + stamp
	}
	return "/assets/" + name
}

var shellAssets = []string{"style.css", "scheme.js", "htmx.js", "helpers.js", "keys.js", "print.css",
	"favicon-32x32.png", "icon-192.png", "icon-512.png"}

// A page carries the digest so a tab on an older shell can tell and load the page whole.
func (s *Server) shell() ([]string, string) {
	urls := make([]string, 0, len(shellAssets)+1)
	version := sha256.New()
	for _, name := range shellAssets {
		u := s.AssetURL(name)
		urls = append(urls, u)
		version.Write([]byte(u))
	}
	return urls, hex.EncodeToString(version.Sum(nil))[:12]
}

// Disk overrides are re-read when their mtime or size changes, so local CSS edits show on reload.
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
	// The manifest and the worker say nothing about any account and are fetched before sign-in.
	return path == "/login" || path == "/manifest.webmanifest" ||
		path == "/sw.js" ||
		strings.HasPrefix(path, "/assets/")
}

func redirectToLogin(ctx *Context) error {
	path := ctx.Request().URL.Path
	to := "/login"
	// Only a GET can be resumed after login; a POST-only route must not be replayed as a GET.
	if ctx.Request().Method == http.MethodGet && path != "/" && path != "/login" {
		to += "?next=" + QueryValue(ctx.Request().URL.String())
	}
	return ctx.Redirect(http.StatusFound, to)
}

// A reader told "internal error" checks their password, so this names the server and the wait.
func renderUpstreamDown(ctx *Context, cause UpstreamError) error {
	type upstreamRenderData struct {
		BaseRenderData
		Service string
		Seconds string
		// Waited separates "no answer" from "no connection"; only the first has a number.
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
	if isPublic(ctx.Request().URL.Path) {
		return next(ctx)
	} else {
		return redirectToLogin(ctx)
	}
}

// ForgetAccount tells every feature that asked that an account has signed out.
func (s *Server) ForgetAccount(username string) {
	for _, forget := range s.OnAccountGone {
		forget(username)
	}
}

// OnAccountReady runs when an account is signed in, including one restored from remembered credentials.
type AccountReadyFunc func(*Context, *Login)

type Options struct {
	Upstreams  []string
	Theme      string
	ThemesPath string
	Debug      bool
	// LoginKey seals remembered sessions, settings kept here and the cached copies.
	LoginKey *fernet.Key
	// Cache keeps what the servers answered.
	Cache bool
	// CacheDir keeps sealed copies of remote calendars and address books; empty keeps none.
	CacheDir string
	// DataDir holds alborz.db; empty is the working directory.
	DataDir string
	// StrictNotices fails an action that redirects with no notice instead of only logging it.
	StrictNotices bool
	// PrivateServices lets an account name a server on plain HTTP or a private address.
	PrivateServices bool
	// TrustedProxies are the proxies whose X-Forwarded-For names the reader.
	TrustedProxies []*net.IPNet
	// PublicURL is where readers reach alborz, for links in mail and for other clients.
	PublicURL *url.URL
	// ProjectURL is where the footer's name links; empty by default.
	ProjectURL string
}

// CacheFor is how long a cache keeps what it holds: d, or zero with caching off.
func (o *Options) CacheFor(d time.Duration) time.Duration {
	if !o.Cache {
		return 0
	}
	return d
}

// New creates a new server; funcs are the template functions the theme calls.
func New(e *echo.Echo, options *Options, funcs template.FuncMap) (*Server, error) {
	s, err := newServer(e, options)
	if err != nil {
		return nil, err
	}
	s.funcs = funcs

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

		if code == http.StatusNotFound {
			if actx, ok := ctx.Get("context").(*Context); ok {
				message := err.Error()
				var missing *notFoundError
				if errors.As(err, &missing) {
					message = actx.Tf(missing.key, missing.args...)
				} else if he != nil {
					if m, ok := he.Message.(string); ok {
						message = m
					}
				}
				if err := RenderInfo(actx, code, message); err == nil {
					return
				}
			}
		}

		// An upstream that did not answer is not this program failing, so it gets its own page rather than a 500.
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

		ctx.Logger().Errorf("%s %s: %v", ctx.Request().Method, ctx.Request().URL.Path, err)
	}

	e.Pre(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ectx echo.Context) error {
			s.mutex.RLock()
			err := next(ectx)
			s.mutex.RUnlock()
			return err
		}
	})

	// The event stream is skipped: a compressor holds it back.
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Skipper: func(c echo.Context) bool {
			p := c.Request().URL.Path
			return strings.HasSuffix(p, ".png") || strings.HasSuffix(p, "/raw") || p == "/events"
		},
		MinLength: 1024,
	}))

	// Lax cookies still let a same-site sibling post, so cross-origin POSTs are refused here.
	e.Use(echo.WrapMiddleware(http.NewCrossOriginProtection().Handler))

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ectx echo.Context) error {
			// `style-src 'unsafe-inline'` is required for e-mails with embedded stylesheets.
			ectx.Response().Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'self'; form-action 'self'; base-uri 'none'")
			ectx.Response().Header().Set("X-Frame-Options", "SAMEORIGIN")
			ectx.Response().Header().Set("X-Content-Type-Options", "nosniff")
			// DNS prefetching has privacy implications
			ectx.Response().Header().Set("X-DNS-Prefetch-Control", "off")
			// Pages carry no validator, so browsers would reuse stale ones; the asset handler upgrades stamped URLs to immutable.
			ectx.Response().Header().Set("Cache-Control", "no-cache")
			return next(ectx)
		}
	})

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ectx echo.Context) error {
			ctx := &Context{Context: ectx, Server: s}
			ctx.Set("context", ctx)
			ctx.installTiming()
			ctx.SetRequest(ctx.Request().WithContext(withStoreMemo(ctx.Request().Context())))

			// A language in the URL is kept, so the next click does not fall back to the old one.
			if lang := queryLanguage(ectx); lang != "" {
				if err := ctx.SetLanguage(lang); err != nil {
					ctx.Logger().Printf("profile: keeping the language: %v", err)
				}
			}

			// A locked session does not sign its remembered accounts back in for whoever holds the browser.
			path, now := ctx.Request().URL.Path, time.Now()
			// Pages saved offline are read with no server to ask for the passkey.
			session := ctx.lookupSession()
			if session != nil && session.locks() && !isPublic(path) {
				ctx.Response().Header().Set("Cache-Control", "no-store")
			}
			if session != nil && session.Locked(now) {
				if lockOpen(path) {
					return next(ctx)
				}
				return redirectToUnlock(ctx)
			} else if session != nil && !isBackground(ctx.Request()) {
				session.act(now)
				if session.note(ctx.Request().UserAgent(), requestAddress(ctx.RealIP())) {
					if err := s.Sessions.Save(session); err != nil {
						ctx.Logger().Printf("failed to write the session: %v", err)
					}
				}
			}

			var login *Login
			if live := ctx.accountLogins(); len(live) > 0 {
				login = live[0]
			}

			// An intercepted POST cannot be resumed after login, so remembered accounts sign back in first.
			if login == nil && !isPublic(ctx.Request().URL.Path) {
				if ctx.RestoreRememberedAccounts() {
					login = ctx.Login
					// A restore is not one account timing out, so none is named.
					ctx.lostAccounts = nil
				}
			}
			if login == nil {
				return handleUnauthenticated(next, ctx)
			}
			ctx.Login = login
			// Every signed-in account stays alive: the rail lists them all.
			logins := ctx.accountLogins()
			for _, login := range logins {
				login.ping()
			}
			ctx.Login = logins[0]
			ctx.DefaultLogin = logins[0]
			// An account that timed out while others stayed is said once, on the page.
			if len(ctx.lostAccounts) > 0 {
				ctx.Notify(Notice{Kind: NoticeWarning,
					Text: ctx.Tf("notice.accountexpired", len(ctx.lostAccounts), strings.Join(ctx.lostAccounts, ", "))})
			}

			// The URL carries the account (ADR 0001); nothing sticks between requests.
			if acct := ctx.QueryParam("account"); acct != "" {
				login := ctx.LoginFor(acct)
				if login == nil {
					return RenderInfo(ctx, http.StatusOK, fmt.Sprintf(ctx.T("account.notsignedin"), acct))
				}
				ctx.Login = login
				ctx.urlAccount = acct
			} else if len(logins) > 1 {
				ctx.Unified = true
			}

			return next(ctx)
		}
	})

	// A POST answered with a redirect and no notice is a handler that forgot.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ectx echo.Context) error {
			err := next(ectx)
			status := ectx.Response().Status
			if err != nil || ectx.Request().Method != http.MethodPost || status < 300 || status >= 400 {
				return err
			}
			ctx := ectx.(*Context)
			if ctx.quiet {
				return err
			}
			if session := ctx.lookupSession(); session != nil && session.HasNotice() {
				return err
			}
			ctx.Logger().Printf("POST %s answered %d with no notice", ctx.Request().URL.Path, status)
			if s.Options.StrictNotices {
				return fmt.Errorf("POST %s answered %d with no notice: say what it did, or ctx.Quiet()",
					ctx.Request().URL.Path, status)
			}
			return err
		}
	})

	// Generated so the name under the icon is BrandName.
	e.GET("/manifest.webmanifest", func(ectx echo.Context) error {
		icon := func(name, purpose string) map[string]string {
			out := map[string]string{"src": s.AssetURL(name), "sizes": "512x512", "type": "image/png"}
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

	// A stream would hold the read lock a reload waits on for as long as a page is open; nothing it reads is swapped by a reload.
	e.GET("/events", func(ectx echo.Context) error {
		s.mutex.RUnlock()
		defer s.mutex.RLock()
		return handleEvents(ectx.Get("context").(*Context))
	})
	e.Server.RegisterOnShutdown(s.Changes.Close)

	// Served here rather than under /assets/: a worker's scope is the path it is served from.
	e.GET("/sw.js", func(ectx echo.Context) error {
		urls, version := s.shell()
		body := fmt.Sprintf(serviceWorker, version, mustJSON(urls))
		ectx.Response().Header().Set("Content-Type", "text/javascript; charset=utf-8")
		return ectx.String(http.StatusOK, body)
	})

	// The theme directory on disk overrides the embedded theme file by file.
	embeddedAssets, err := fs.Sub(embeddedTheme, "themes/alborz/assets")
	if err != nil {
		return nil, err
	}
	e.GET("/assets/*", func(ectx echo.Context) error {
		name := strings.TrimPrefix(path.Clean("/"+ectx.Param("*")), "/")
		if name == "" {
			return echo.ErrNotFound
		}
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
