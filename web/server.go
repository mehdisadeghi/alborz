package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"net/mail"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"git.mehdix.org/alborz"
)

// Server is alborz as the web serves it.
type Server struct {
	*alborz.Server

	routes routes
	// serving is set once the routes are final.
	serving bool

	funcs template.FuncMap
	// loaded is what a reload swaps: the templates and custom.css.
	loaded atomic.Pointer[loaded]

	// trusted are the proxies whose X-Forwarded-For names the reader and X-Forwarded-Proto the scheme.
	trusted []netip.Prefix
	// projectURL is where the footer's name links; strictNotices fails a POST that redirects saying nothing.
	projectURL    string
	strictNotices bool

	// OnAccountReady is called when an account signs in.
	OnAccountReady []AccountReadyFunc

	// additions are what features add to another's pages, by the type the page hands them (Extend).
	additions map[reflect.Type][]any
	// OnServers puts each feature's servers among the account's.
	OnServers []func(*Context, *Servers) error
	// Searchers are the sections the search across sections asks, in the order it shows them.
	Searchers []Searcher

	// Status are the segments the status bar offers, in the order it draws them.
	Status []Segment
	// Addons are the add-ons on offer.
	Addons []Addon

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

	// base is the stylesheet every theme overlays, standing in for a theme when none is chosen.
	base   Theme
	themes []Theme
}

type loaded struct {
	renderer *renderer
	// custom is the deployment's own stylesheet, loaded last so it can override the theme without forking it.
	custom      []byte
	customStamp string
}

// Options are alborz's, and what the web adds.
type Options struct {
	alborz.Options
	// TrustedProxies are the addresses or CIDR ranges of the proxies in front, whose X-Forwarded-For names the reader and X-Forwarded-Proto the scheme.
	TrustedProxies []string
	// ProjectURL is where the footer's name links; empty prints the name alone.
	ProjectURL string
	// StrictNotices fails an action that redirects with no notice instead of only logging it.
	StrictNotices bool
}

// New creates a new server; funcs are the functions the templates call, beside url.
func New(logger *log.Logger, options Options, funcs template.FuncMap) (*Server, error) {
	var trusted []netip.Prefix
	for _, p := range options.TrustedProxies {
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			a, aerr := netip.ParseAddr(p)
			if aerr != nil {
				return nil, fmt.Errorf("invalid trusted proxy %q: %v", p, aerr)
			}
			prefix = netip.PrefixFrom(a, a.BitLen())
		}
		trusted = append(trusted, prefix.Masked())
	}
	core, err := alborz.New(logger, options.Options)
	if err != nil {
		return nil, err
	}
	s := &Server{Server: core, assets: make(map[string]assetStamp), trusted: trusted,
		projectURL: options.ProjectURL, strictNotices: options.StrictNotices,
		routes: routes{mux: http.NewServeMux(), names: map[string]string{}}}
	s.files = os.DirFS(templatesDir)
	if !options.Debug {
		files, err := fs.Sub(alborz.Templates, templatesDir)
		if err != nil {
			return nil, err
		}
		s.files = files
	}
	if s.base, s.themes, err = readThemes(s.files); err != nil {
		return nil, err
	}
	s.additions = map[reflect.Type][]any{}
	s.Sessions = newSessions(core.SessionRecords, core.Pushes)
	core.KeepLogin = s.keepLogin
	s.funcs = maps.Clone(funcs)
	s.funcs["url"] = s.templateURL
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Handler serves the routes mounted; a route or template link that cannot work stops it.
func (s *Server) Handler() (http.Handler, error) {
	if err := errors.Join(s.routes.errs...); err != nil {
		return nil, err
	}
	if err := s.checkURLs(s.renderer()); err != nil {
		return nil, err
	}
	s.serving = true
	return s.routes.mux, nil
}

func (s *Server) trusts(ip netip.Addr) bool {
	for _, n := range s.trusted {
		if n.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

func (s *Server) renderer() *renderer { return s.loaded.Load().renderer }

func (s *Server) load() error {
	renderer, err := loadTemplates(s.files, s.funcs)
	if err != nil {
		return fmt.Errorf("failed to load templates: %v", err)
	}
	// Once serving, a template linking nowhere is refused before it replaces a working one.
	if s.serving {
		if err := s.checkURLs(renderer); err != nil {
			return err
		}
	}
	custom, err := os.ReadFile(filepath.Join(filepath.Dir(s.DataPath), customCSS))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.loaded.Store(&loaded{renderer: renderer, custom: custom, customStamp: stampOf(custom)})
	return nil
}

// Reload reads the templates and custom.css from disk again.
func (s *Server) Reload() error {
	s.Logger().Printf("Reloading templates and custom.css")
	return s.load()
}

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

var aLongTimeAgo = time.Unix(233431200, 0)

// Relocate makes the page load path whole instead of the piece it asked for.
func (ctx *Context) Relocate(path string) error {
	ctx.Response().Header().Set("HX-Redirect", path)
	return ctx.NoContent(http.StatusOK)
}

// Redraw answers a piece whose row left its list: the list at path is swapped into main where the reader is,
// with no step in history, and the notice comes with it (ADR 13).
func (ctx *Context) Redraw(path string) error {
	at, _ := json.Marshal(map[string]string{"path": path, "target": "#main", "swap": "innerHTML show:none", "push": "false"})
	ctx.Response().Header().Set("HX-Location", string(at))
	return ctx.NoContent(http.StatusOK)
}

// Origin is where alborz is reached from outside: -public-url, else this request's address.
func (ctx *Context) Origin() string {
	if u := ctx.Server.PublicURL; u != nil {
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
		return s.mustPath("core.asset", name) + "?v=" + stamp
	}
	return s.mustPath("core.asset", name)
}

var shellAssets = []string{"style.css", "scheme.js", "htmx.js", "helpers.js", "keys.js", "print.css",
	"favicon-32x32.png", "icon-192.png", "icon-512.png"}

// A page carries the digest so a tab on an older shell can tell and load the page whole.
// shellHeader names the shell an answer was built for (ADR 19).
const shellHeader = "Alborz-Shell"

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
		return s.loaded.Load().customStamp
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

func redirectToLogin(ctx *Context) error {
	if unasked(ctx) {
		return ctx.NoContent(http.StatusNoContent)
	}
	path, to := ctx.Request().URL.Path, ctx.Path("auth.login")
	// Only a GET can be resumed after login; a POST-only route must not be replayed as a GET.
	if ctx.Request().Method == http.MethodGet && path != ctx.Path("mail.home") && path != to {
		to += "?next=" + QueryValue(ctx.Request().URL.String())
	}
	// Signing in takes the window; followed by htmx, it would land inside the piece asked for.
	if ctx.Fragment() {
		return ctx.Relocate(to)
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
		Service:        ctx.T("upstream." + cause.Service),
		Seconds:        fmt.Sprintf("%d", int(cause.After.Seconds())),
		Waited:         cause.After > 0,
		Retry:          ctx.Request().URL.RequestURI(),
	}
	data.BaseRenderData.WithTitle(ctx.Tf("upstream.title", data.Service))
	return ctx.Render(http.StatusBadGateway, "upstream", &data)
}

// OnAccountReady runs when an account is signed in, including one restored from remembered credentials.
type AccountReadyFunc func(*Context, *alborz.Login)

// Extend adds a feature's part to another feature's page: every page that hands its readers a T runs add.
func Extend[T any](s *Server, add func(*Context, T) error) {
	k := reflect.TypeFor[T]()
	s.additions[k] = append(s.additions[k], add)
}

// Extended runs what the features added to pages that hand them a T, in the order they were added.
func Extended[T any](ctx *Context, page T) error {
	for _, add := range ctx.Server.additions[reflect.TypeFor[T]()] {
		if err := add.(func(*Context, T) error)(ctx, page); err != nil {
			return err
		}
	}
	return nil
}
