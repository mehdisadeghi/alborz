package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/mail"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"github.com/klauspost/compress/gzhttp"
	"github.com/labstack/echo/v4"
)

// Server is alborz as the web serves it.
type Server struct {
	*alborz.Server
	e *echo.Echo

	mutex sync.RWMutex
	funcs template.FuncMap

	// OnAccountReady is called when an account signs in.
	OnAccountReady []AccountReadyFunc

	// OnMessage and OnServers add another feature's part to the message and servers pages.
	OnMessage []func(*Context, RenderData) error
	OnServers []func(*Context, RenderData) error

	// Suggest offers addresses to write to.
	Suggest []func(*Context) []string

	// OnSent hears of a message that left, from the account that sent it, with everyone it went to.
	OnSent []func(*Context, *alborz.Login, []*mail.Address) error

	assetsMu sync.Mutex
	assets   map[string]assetStamp

	// Sessions are the browsers signed in.
	Sessions *Sessions

	// files is the templates tree: read from disk in debug mode, so an edit shows on reload, else embedded.
	files fs.FS

	themes []Theme

	// custom is the deployment's own stylesheet, loaded last so it can override the theme without forking it.
	custom      []byte
	customStamp string
}

// What a launcher paints before any stylesheet has loaded.
const (
	brandColor      = "#ffffff"
	brandBackground = "#ffffff"
)

const (
	themeCookieName    = "alborz_theme"
	alignCookieName    = "alborz_align"
	textSizeCookieName = "alborz_text"
	// timezoneCookieName is written by the page: only the browser knows its zone.
	timezoneCookieName = "alborz_tz"
)

const (
	preferenceCookieLife = 365 * 24 * time.Hour
)

func (s *Server) hasTheme(name string) bool {
	for _, t := range s.themes {
		if t.Name == name {
			return true
		}
	}
	return false
}

const (
	customCSS = "custom.css"
	// templatesDir is the tree's place in the source, where debug mode reads it.
	templatesDir = "templates"
)

type assetStamp struct {
	key   string
	stamp string
}

func (s *Server) load() error {
	renderer, err := loadTemplates(s.files, s.funcs)
	if err != nil {
		return fmt.Errorf("failed to load templates: %v", err)
	}
	custom, err := os.ReadFile(filepath.Join(filepath.Dir(s.DataPath), customCSS))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.e.Renderer = renderer
	s.custom = custom
	s.customStamp = stampOf(custom)
	return nil
}

// Reload reads the templates and custom.css from disk again.
func (s *Server) Reload() error {
	s.e.Logger.Printf("Reloading templates and custom.css")
	return s.load()
}

// HandlerFunc serves a request.
type HandlerFunc func(*Context) error

// Router adds a feature's routes to the server.
type Router struct{ e *echo.Echo }

func (s *Server) Router() Router { return Router{s.e} }

// Pre answers requests ahead of routing, sessions and cookies, inside the reload lock.
func (s *Server) Pre(mw echo.MiddlewareFunc) { s.e.Pre(mw) }

// MaxFormSize bounds a form with no file: several times the longest text one carries, a Sieve script.
const MaxFormSize = 4 << 20

// FormFieldsSize is the room a file's form takes beside it, for the other fields and the part headers.
const FormFieldsSize = 64 << 10

// formMemory is what a parsed form holds in memory before files go to disk, as echo's own parse.
const formMemory = 32 << 20

func (r Router) GET(path string, h HandlerFunc) {
	r.add(http.MethodGet, path, MaxFormSize, h, refuseTooLarge)
}

func (r Router) POST(path string, h HandlerFunc) {
	r.add(http.MethodPost, path, MaxFormSize, h, refuseTooLarge)
}

// Upload takes a form carrying files up to limit; one past it is answered by over, h never seeing its empty fields.
func (r Router) Upload(path string, limit int64, h, over HandlerFunc) {
	r.add(http.MethodPost, path, limit, h, over)
}

// Stream leaves a body up to limit to the handler, to read as it arrives and meet *http.MaxBytesError where it stops.
func (r Router) Stream(path string, limit int64, h HandlerFunc) {
	r.add(http.MethodPost, path, limit, h, nil)
}

func refuseTooLarge(*Context) error { return echo.NewHTTPError(http.StatusRequestEntityTooLarge) }

// add bounds the body before the handler reads it, so an upload past the limit is refused unread.
// A form is parsed first, since one cut short reads as empty fields; with no over, the handler streams it.
func (r Router) add(method, path string, limit int64, h, over HandlerFunc) {
	r.e.Add(method, path, func(ectx echo.Context) error {
		ctx := ectx.(*Context)
		req := ctx.Request()
		req.Body = http.MaxBytesReader(ctx.Response(), req.Body, limit)
		if method != http.MethodPost || over == nil {
			return h(ctx)
		}
		err := req.ParseMultipartForm(formMemory)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return over(ctx)
		} else if err != nil && !errors.Is(err, http.ErrNotMultipart) {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}
		return h(ctx)
	})
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
	Login         *alborz.Login
	defaultLogin  *alborz.Login // the first listed account, for a page that needs one and was given none

	// Unified marks the merged all-accounts view; Login then anchors to one of its accounts.
	Unified bool

	// The accounts cookie only reflects the request, so account changes within one handler read their own writes here.
	accounts       []*alborz.Login
	accountsLoaded bool
	lostAccounts   []string

	// A cookie written to the answer is not in the request, so a handler reads its own writes here.
	prefs map[string]string

	timing *alborz.Timing

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

// A file is read again when its mtime or size changes, so a CSS edit in debug mode shows on reload.
func (s *Server) assetStamp(name string) string {
	if name == customCSS {
		return s.customStamp
	}
	file := path.Join("assets", name)
	info, err := fs.Stat(s.files, file)
	if err != nil {
		return ""
	}
	key := fmt.Sprintf("%d.%d", info.ModTime().UnixNano(), info.Size())

	s.assetsMu.Lock()
	defer s.assetsMu.Unlock()
	if e, ok := s.assets[name]; ok && e.key == key {
		return e.stamp
	}

	content, err := fs.ReadFile(s.files, file)
	if err != nil {
		return ""
	}
	stamp := stampOf(content)
	s.assets[name] = assetStamp{key: key, stamp: stamp}
	return stamp
}

func stampOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:4])
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
func renderUpstreamDown(ctx *Context, cause alborz.UpstreamError) error {
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
	return ctx.Render(http.StatusBadGateway, "upstream", &data)
}

func handleUnauthenticated(next echo.HandlerFunc, ctx *Context) error {
	if isPublic(ctx.Request().URL.Path) {
		return next(ctx)
	} else {
		return redirectToLogin(ctx)
	}
}

// OnAccountReady runs when an account is signed in, including one restored from remembered credentials.
type AccountReadyFunc func(*Context, *alborz.Login)

// New creates a new server; funcs are the functions the templates call.
func New(e *echo.Echo, options *alborz.Options, funcs template.FuncMap) (*Server, error) {
	core, err := alborz.New(e.Logger, options)
	if err != nil {
		return nil, err
	}
	s := &Server{Server: core, e: e, assets: make(map[string]assetStamp)}
	e.IPExtractor = echo.ExtractIPDirect()
	if len(options.TrustedProxies) > 0 {
		// echo trusts every private address by default, and anyone on such a network could write the header.
		trust := []echo.TrustOption{echo.TrustLoopback(false), echo.TrustLinkLocal(false), echo.TrustPrivateNet(false)}
		for _, n := range options.TrustedProxies {
			trust = append(trust, echo.TrustIPRange(n))
		}
		e.IPExtractor = echo.ExtractIPFromXFFHeader(trust...)
	}
	s.files = os.DirFS(templatesDir)
	if !options.Debug {
		files, err := fs.Sub(alborz.Templates, templatesDir)
		if err != nil {
			return nil, err
		}
		s.files = files
	}
	s.themes = readThemes(s.files)
	s.Sessions = newSessions(core.SessionRecords)
	s.funcs = funcs

	if err := s.load(); err != nil {
		return nil, err
	}

	e.HTTPErrorHandler = func(err error, ctx echo.Context) {
		code := http.StatusInternalServerError
		var he *echo.HTTPError
		if errors.As(err, &he) {
			code = he.Code
		} else if errors.Is(err, alborz.ErrNotFound) {
			code = http.StatusNotFound
		} else if errors.Is(err, alborz.ErrIMAPQueueFull) {
			code = http.StatusServiceUnavailable
		}

		if code == http.StatusNotFound {
			if actx, ok := ctx.Get("context").(*Context); ok {
				message := err.Error()
				var missing *alborz.NotFoundError
				if errors.As(err, &missing) {
					message = actx.Tf(missing.Key, missing.Args...)
				} else if he != nil {
					if m, ok := he.Message.(string); ok {
						message = m
					}
				}
				if err := renderInfo(actx, code, message); err == nil {
					return
				}
			}
		}

		// An upstream that did not answer is not this program failing, so it gets its own page rather than a 500.
		var upstream alborz.UpstreamError
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

		if err := ctx.Render(code, "error", &rdata); err != nil {
			ctx.Logger().Error(fmt.Errorf(
				"Error occured rendering error page: %w. How meta.", err))
		}

		ctx.Logger().Errorf("%s %s: %v", ctx.Request().Method, ctx.Request().URL.Path, err)
	}

	e.Pre(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ectx echo.Context) error {
			s.mutex.RLock()
			// Deferred: a download that fails midway aborts with a panic.
			defer s.mutex.RUnlock()
			return next(ectx)
		}
	})

	compress, err := gzhttp.NewWrapper(gzhttp.EnableZstd(false))
	if err != nil {
		return nil, err
	}
	// The event stream is skipped: a compressor holds it back.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ectx echo.Context) error {
			p := ectx.Request().URL.Path
			if strings.HasSuffix(p, ".png") || strings.HasSuffix(p, "/raw") || p == "/events" {
				return next(ectx)
			}
			res := ectx.Response()
			plain := res.Writer
			var err error
			compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				res.Writer = w
				err = next(ectx)
			})).ServeHTTP(plain, ectx.Request())
			// The error page is written after the compressor has closed.
			res.Writer = plain
			return err
		}
	})

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
			ctx.SetRequest(ctx.Request().WithContext(alborz.WithStoreMemo(ctx.Request().Context())))

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
				if session.note(ctx.Request().UserAgent(), alborz.RequestAddress(ctx.RealIP())) {
					if err := s.Sessions.Save(session); err != nil {
						ctx.Logger().Printf("failed to write the session: %v", err)
					}
				}
			}

			var login *alborz.Login
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
				login.Ping()
			}
			ctx.Login = logins[0]
			ctx.defaultLogin = logins[0]
			// An account that timed out while others stayed is said once, on the page.
			if len(ctx.lostAccounts) > 0 {
				ctx.Notify(Notice{Kind: NoticeWarning,
					Text: ctx.Tf("notice.accountexpired", len(ctx.lostAccounts), strings.Join(ctx.lostAccounts, ", "))})
			}

			// The URL carries the account (ADR 0001); nothing sticks between requests.
			if acct := ctx.QueryParam("account"); acct != "" {
				login := ctx.LoginFor(acct)
				if login == nil {
					return renderInfo(ctx, http.StatusOK, fmt.Sprintf(ctx.T("account.notsignedin"), acct))
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
			if session := ctx.lookupSession(); session != nil && session.hasNotice() {
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
			"name":             alborz.BrandName,
			"short_name":       alborz.BrandName,
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

	assets, err := fs.Sub(s.files, "assets")
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
		if name == customCSS {
			if len(s.custom) == 0 {
				return echo.ErrNotFound
			}
			return ectx.Blob(http.StatusOK, "text/css; charset=utf-8", s.custom)
		}
		http.ServeFileFS(ectx.Response(), ectx.Request(), assets, name)
		return nil
	})

	return s, nil
}
