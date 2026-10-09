package web

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"slices"
	"strings"
	"text/template/parse"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/calsys"
	"git.mehdix.org/alborz/internal/dates"
	"git.mehdix.org/alborz/internal/lang"
	"git.mehdix.org/alborz/internal/locale"
)

// GlobalRenderData contains data available in all templates.
type GlobalRenderData struct {
	Path []string
	URL  *url.URL

	LoggedIn bool
	// SearchBlocks are the header search's sections, waiting to be asked.
	SearchBlocks []SearchBlock
	// SearchViews are the header search's filter menu.
	SearchViews []FilterRow

	Username string
	Accounts []Account

	Title string

	// Asset builds a theme asset's version-stamped URL.
	Asset func(name string) string

	Notice *Notice

	// Version names the build.
	Version string
	// Shell is the digest of what the head loads.
	Shell string
	// LockSeconds is the idle time before the session locks, zero without a passkey.
	LockSeconds int
	// Revision names the running binary.
	Revision string
	// Brand is the product's name as a person reads it.
	Brand string
	// ProjectURL is where the footer's name links; empty prints the name as text.
	ProjectURL string
	// Language is the explicit choice, empty while following the browser.
	Language        string
	LanguageChoices []locale.Choice
	Year            int
	// Primary is the calendar the page counts in, Secondary the one glossed beside it, empty for none.
	Primary   string
	Secondary string

	Timezone *time.Location

	FirstDayOfWeek time.Weekday

	Unified bool

	AlignByScript bool

	// URLAccount keeps links pointing into the request's account; empty otherwise.
	URLAccount string

	From string

	// Scope and Merged are the account the nav, switcher and rail follow, and whether it is all of them.
	Scope  string
	Merged bool
	// Item says the page shows one object; Own is its URL without From (ADR 37).
	Item bool
	Own  string

	Theme string
	// ThemeAsset is the chosen theme's asset path, including an add-on prefix when applicable.
	ThemeAsset string
	// AddonStyles are styles supplied by registered add-ons.
	AddonStyles []string

	// SchemePinned hides the scheme toggle for a theme written for one scheme only.
	SchemePinned bool
	// ThemeColors are the theme's top bar per scheme, which the browser paints under the status bar.
	ThemeColors []ThemeColor
	// Scene names the layers the theme paints behind the page.
	Scene []string

	CustomCSS bool

	// TextSize is empty for the default size.
	TextSize string

	// StatusStart and StatusEnd name the status bar's segments turned on, by the end they sit at;
	// StatusSeconds is how often a page asks them again.
	StatusStart, StatusEnd []string
	StatusSeconds          int
	// Scripts are what the add-ons turned on load.
	Scripts []string

	Lang string

	// Swap says the page answers a swap into main: its main block, and the rail rows it moves (ADR 13).
	Swap bool
	// HasRail says the page data carries a feature rail for the shared page shell.
	HasRail bool

	// Tour is the tour open on this page, nil for none; TourStart opens it here (ADR 76).
	Tour      *Tour
	TourStart string
}

// Layer reports whether the theme's scene has the named layer.
func (g *GlobalRenderData) Layer(name string) bool {
	return slices.Contains(g.Scene, name)
}

// Dir is the writing direction of the UI language.
func (g *GlobalRenderData) Dir() string {
	return g.T("language.dir")
}

// AccountQuery keeps a link in the request's account.
func (g *GlobalRenderData) AccountQuery() template.URL {
	return AccountParam("?", g.URLAccount)
}

// AccountParam names the account in a link; template.URL, so the escaper normalises rather than escapes the address.
func AccountParam(sep, username string) template.URL {
	if username == "" {
		return ""
	}
	return template.URL(sep + "account=" + QueryValue(username))
}

// BaseRenderData is the base type for templates, embedded in template-specific ones.
type BaseRenderData struct {
	GlobalData GlobalRenderData
}

// Global implements RenderData.
func (brd *BaseRenderData) Global() *GlobalRenderData {
	return &brd.GlobalData
}

// T translates a namespaced string key into the UI language.
func (brd *BaseRenderData) T(key string) string {
	return locale.T(brd.GlobalData.Lang, key)
}

func (g *GlobalRenderData) dates() dates.Reader {
	return dates.Reader{Lang: g.Lang, Loc: g.Timezone, Primary: g.Primary, Secondary: g.Secondary}
}

func (g *GlobalRenderData) Since(t time.Time) string { return g.dates().Since(t) }

func (g *GlobalRenderData) FormatDate(t time.Time) string { return g.dates().Full(t) }

func (g *GlobalRenderData) ShortDate(t time.Time) string { return g.dates().Short(t) }

func (g *GlobalRenderData) ShortDay(t time.Time) string { return g.dates().ShortDay(t) }

func (g *GlobalRenderData) FormatTime(t time.Time) string { return g.dates().Clock(t) }

func (g *GlobalRenderData) InTimezone(t time.Time) time.Time { return g.dates().In(t) }

func (g *GlobalRenderData) WeekdayName(d time.Weekday) string { return g.dates().Weekday(d) }

func (g *GlobalRenderData) MonthName(t time.Time) string { return g.dates().Month(t) }

func (g *GlobalRenderData) ShortMonth(cal calsys.System, month int) string {
	return g.dates().ShortMonth(cal, month)
}

func (g *GlobalRenderData) DayMonth(cal calsys.System, day, month int) dates.DayMonth {
	return g.dates().DayMonth(cal, day, month)
}

func (g *GlobalRenderData) PrimaryCalendar() calsys.System { return g.dates().Calendar() }

func (g *GlobalRenderData) SecondaryCalendar(t time.Time) (calsys.System, bool) {
	return g.dates().SecondaryCalendar(t)
}

func (g *GlobalRenderData) SecondaryDate(t time.Time) string { return g.dates().SecondaryDate(t) }

func (g *GlobalRenderData) MonthYear(t time.Time) string { return g.dates().MonthYear(t) }

func (g *GlobalRenderData) MonthYearShort(t time.Time) string { return g.dates().MonthYearShort(t) }

func (g *GlobalRenderData) LongDate(t time.Time) string { return g.dates().Long(t) }

func (g *GlobalRenderData) InputDate(t time.Time) dates.Field { return g.dates().Input(t) }

func (g *GlobalRenderData) InputDateTime(t time.Time) dates.Field { return g.dates().InputTime(t) }

func (g *GlobalRenderData) DateExample(f dates.Field) string { return g.dates().Example(f) }

func (g *GlobalRenderData) DateTimeExample(f dates.Field) string { return g.dates().ExampleTime(f) }

func (g *GlobalRenderData) InputDay(iso string) dates.Field { return g.dates().Day(iso) }

// ContentLang names the language of interface text, empty where it matches the page.
func (g *GlobalRenderData) ContentLang(s string) string {
	return lang.OfContent(s, g.Lang)
}

// MessageLang is ContentLang for what a message itself says.
func (g *GlobalRenderData) MessageLang(s string) string {
	return lang.OfMessage(s, g.Lang)
}

// shortAccount names an account by its domain unless two accounts share it.
func shortAccount(account string, accounts []Account) string {
	_, domain, ok := strings.Cut(account, "@")
	if !ok {
		return account
	}
	for _, a := range accounts {
		if a.Username != account && strings.HasSuffix(a.Username, "@"+domain) {
			return account
		}
	}
	return domain
}

// AccountLabel is ShortAccount for the accounts of the page.
func (g GlobalRenderData) AccountLabel(account string) string {
	return shortAccount(account, g.Accounts)
}

// Value receivers: templates box the global in a tuple, leaving pointer methods out of reach.
func (g GlobalRenderData) Num(n int) string { return locale.Num(g.Lang, n) }

func (g GlobalRenderData) FormatYear(n int) string { return locale.Year(g.Lang, n) }

func (g GlobalRenderData) T(key string) string { return locale.T(g.Lang, key) }

func (g GlobalRenderData) Tf(key string, args ...interface{}) string {
	return locale.Tf(g.Lang, key, args...)
}

// RenderData is implemented by template data structs.
type RenderData interface {
	// GlobalData returns a pointer to the global render data.
	Global() *GlobalRenderData
}

// NewBaseRenderData initializes a new BaseRenderData.
func NewBaseRenderData(ctx *Context) *BaseRenderData {
	reader := ctx.Dates()
	lang := reader.Lang
	global := GlobalRenderData{
		Path:           strings.Split(ctx.Request().URL.Path, "/")[1:],
		Title:          alborz.BrandName,
		URL:            withoutTour(ctx.Request().URL),
		FirstDayOfWeek: alborz.Reading{}.WeekStart(),
		Lang:           lang,
		Swap:           ctx.PartialFor("main"),

		Asset: ctx.Server.AssetURL,
	}

	global.Version, global.Revision = alborz.Build()
	_, global.Shell = ctx.Server.shell()
	if s := ctx.lookupSession(); s != nil {
		if lock := s.Lock(); len(lock.Passkeys) > 0 {
			global.LockSeconds = int(lock.Wait() / time.Second)
		}
	}
	// A person reads the brand in their own script; what a machine reads stays BrandName.
	global.Brand = locale.T(lang, "brand.name")
	global.ProjectURL = ctx.Server.projectURL
	global.Language = ctx.language()
	global.LanguageChoices = locale.Choices()
	global.Year = time.Now().Year()
	global.Primary, global.Secondary, global.Timezone = reader.Primary, reader.Secondary, reader.Loc
	global.Theme = ctx.Theme()
	global.ThemeAsset = ctx.theme().Asset
	global.AddonStyles = ctx.Server.addonStyles()
	global.TextSize = ctx.TextSize()
	global.Unified = ctx.Unified
	global.AlignByScript = ctx.alignByScript()
	global.CustomCSS = len(ctx.Server.loaded.Load().custom) > 0
	theme := ctx.theme()
	global.SchemePinned = theme.Scheme != ""
	global.ThemeColors = theme.Colors
	global.Scene = theme.Scene
	global.URLAccount = ctx.urlAccount
	global.From = ctx.From()
	global.Scope, global.Merged = ctx.urlAccount, ctx.Unified
	// Values.Encode would escape the raw values again.
	req := global.URL
	kept := slices.DeleteFunc(strings.Split(req.RawQuery, "&"), func(p string) bool {
		return p == "" || strings.HasPrefix(p, "from=")
	})
	global.Own = req.EscapedPath()
	if q := strings.Join(kept, "&"); q != "" {
		global.Own += "?" + q
	}
	if u, err := url.Parse(global.From); err == nil && global.From != "" {
		global.Scope = u.Query().Get("account")
		global.Merged = global.Scope == "" && len(ctx.Logins()) > 1
	}
	// A notice belongs to the session, not to a login.
	global.Notice = ctx.popNotice()
	for _, l := range ctx.Logins() {
		if global.Notice == nil && l.Misread() {
			global.Notice = &Notice{Kind: NoticeWarning, Text: locale.T(lang, "notice.misread")}
		}
	}

	if ctx.Login != nil {
		global.LoggedIn = true
		global.Username = ctx.Login.Username()
		global.Accounts = ctx.Accounts()
		global.FirstDayOfWeek = ctx.Reading().WeekStart()
		global.StatusStart, global.StatusEnd = ctx.statusOn()
		global.StatusSeconds = int(statusEvery / time.Second)
		global.SearchBlocks = ctx.waitingBlocks()
		global.SearchViews = ViewRows(ctx, "", false, "")
		global.Scripts = ctx.scripts()
		global.Tour = ctx.tour(global.URL, global.Scope)
		global.TourStart = TourStart(global.URL)
	}

	return &BaseRenderData{GlobalData: global}
}

// Tf is T with a count and arguments, in the plural form the count calls for.
func (ctx *Context) Tf(key string, args ...interface{}) string {
	return locale.Tf(requestLanguage(ctx), key, args...)
}

// T translates a namespaced string key into the request's UI language.
func (ctx *Context) T(key string) string {
	return locale.T(requestLanguage(ctx), key)
}

// PageLanguage is the UI language this request resolves to.
func (ctx *Context) PageLanguage() string {
	return requestLanguage(ctx)
}

// LanguageName is the chosen language in its own name, empty while following the browser.
func (g GlobalRenderData) LanguageName() string {
	for _, c := range g.LanguageChoices {
		if c.Code == g.Language {
			return c.Name
		}
	}
	return ""
}

// The URL's language outranks the cookie, so a page can be handed to a reader of another language.
func queryLanguage(ctx *Context) string {
	if v := ctx.QueryParam("lang"); locale.Supported(v) {
		return v
	}
	return ""
}

func requestLanguage(ctx *Context) string {
	if v := queryLanguage(ctx); v != "" {
		return v
	}
	if v := ctx.language(); v != "" {
		return v
	}
	return locale.Match(ctx.Request().Header.Get("Accept-Language"))
}

// renderInfo renders a full page carrying one explanatory sentence.
func renderInfo(ctx *Context, code int, message string) error {
	data := struct {
		BaseRenderData
		Message string
	}{*NewBaseRenderData(ctx), message}
	return ctx.Render(code, "info", &data)
}

// WithTitle sets the page's own title.
func (brd *BaseRenderData) WithTitle(title string) *BaseRenderData {
	brd.GlobalData.Title = title
	return brd
}

// Refused makes a form's refusal the page's notice (ADR 41); empty text leaves it as it was.
func (brd *BaseRenderData) Refused(text string) *BaseRenderData {
	if text != "" {
		brd.GlobalData.Notice = &Notice{Kind: NoticeFailed, Text: text}
	}
	return brd
}

// WithItem marks the page as showing one object.
func (brd *BaseRenderData) WithItem() *BaseRenderData {
	brd.GlobalData.Item = true
	return brd
}

// Dates is the request's reader: its language, calendars and, signed in, its zone.
func (ctx *Context) Dates() dates.Reader {
	r := dates.Reader{Lang: requestLanguage(ctx)}
	r.Primary, r.Secondary = ctx.calendars()
	// Unset means times keep their stored zones.
	if ctx.Login != nil {
		r.Loc = UserLocation(ctx)
	}
	return r
}

// DateField is the named field as the form sent it.
func (ctx *Context) DateField(name string) dates.Field {
	return dates.Field{Value: strings.TrimSpace(ctx.FormValue(name)), Calendar: ctx.FormValue(name + fieldCalendar)}
}

// fieldCalendar suffixes a date field's name to name the hidden field saying its calendar, as templates/partials/form.html writes it.
const fieldCalendar = "-calendar"

// PageTitle is the browser title: the page's subject and the brand, or the brand alone.
func (g *GlobalRenderData) PageTitle() string {
	brand := g.Brand
	if brand == "" {
		brand = alborz.BrandName
	}
	if g.Title == "" || g.Title == alborz.BrandName {
		return brand
	}
	return g.Title + " - " + brand
}

// renderer parses each page into its own clone of the layout and partials, so pages' blocks never collide.
type renderer struct {
	partials *template.Template
	pages    map[string]*template.Template
}

func (r *renderer) Render(w io.Writer, name string, data interface{}, ctx *Context) error {
	var renderData RenderData
	if data == nil {
		renderData = &struct{ BaseRenderData }{*NewBaseRenderData(ctx)}
	} else {
		var ok bool
		renderData, ok = data.(RenderData)
		if !ok {
			return fmt.Errorf("data passed to template %q doesn't implement RenderData", name)
		}
	}

	// A notice raised after the base data was built belongs to this page, not the next.
	if g := renderData.Global(); g.Notice == nil {
		g.Notice = ctx.popNotice()
	}

	start := time.Now()
	var err error
	if page, ok := r.pages[name]; ok {
		// A swap into main takes the page's own main block, not a second page inside the first (ADR 13).
		block := "layout"
		if renderData.Global().Swap {
			block = "swap"
		}
		err = page.ExecuteTemplate(w, block, data)
	} else {
		err = r.partials.ExecuteTemplate(w, name, data)
	}
	ctx.timing.Add("render", start, time.Now())
	return err
}

// loadTemplates reads layout.html and partials/ into one set, and each file under pages/ into a clone of it,
// named by its path without pages/ and .html.
func loadTemplates(files fs.FS, funcs template.FuncMap, bundles []AddonBundle) (*renderer, error) {
	partials := template.New("").Funcs(funcs)
	owner := map[string]string{}
	type source struct {
		files       fs.FS
		name, owner string
	}
	shared := []source{{files, "layout.html", "layout.html"}}
	collect := func(tree fs.FS, prefix string) error {
		if _, err := fs.Stat(tree, "partials"); errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		return fs.WalkDir(tree, "partials", func(name string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				shared = append(shared, source{tree, name, prefix + name})
			}
			return err
		})
	}
	if err := collect(files, ""); err != nil {
		return nil, err
	}
	for _, bundle := range bundles {
		if err := collect(bundle.Files, "addons/"+bundle.Name+"/"); err != nil {
			return nil, err
		}
	}
	for _, file := range shared {
		src, defined, err := parseFile(file.files, file.name, funcs)
		if err != nil {
			return nil, err
		}
		// html/template lets a second definition replace the first without a word.
		for _, t := range defined {
			if first, ok := owner[t]; ok {
				old := partials.Lookup(t)
				if first != "layout.html" || old == nil || !parse.IsEmptyTree(old.Tree.Root) {
					return nil, fmt.Errorf("%s: %q is defined in %s already", file.owner, t, first)
				}
			}
			owner[t] = file.owner
		}
		if _, err := partials.New(file.owner).Parse(src); err != nil {
			return nil, err
		}
	}

	pages := map[string]*template.Template{}
	addPages := func(tree fs.FS, prefix string) error {
		if _, err := fs.Stat(tree, "pages"); errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		return fs.WalkDir(tree, "pages", func(name string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			src, defined, err := parseFile(tree, name, funcs)
			if err != nil {
				return err
			}
			if !slices.Contains(defined, "main") && !slices.Contains(defined, "body") {
				return fmt.Errorf("%s: page must define main or body", name)
			}
			for _, forbidden := range []string{"layout", "nav", "status-bar"} {
				if slices.Contains(defined, forbidden) {
					return fmt.Errorf("%s: page cannot replace shared %q", name, forbidden)
				}
			}
			// A page fills the layout's blocks, or a partial's empty one; replacing a partial would change it for this page alone.
			for _, t := range defined {
				if s := partials.Lookup(t); s != nil && owner[t] != "layout.html" && !parse.IsEmptyTree(s.Tree.Root) {
					return fmt.Errorf("%s: %q is a partial in %s, not a block", name, t, owner[t])
				}
			}
			key := strings.TrimSuffix(strings.TrimPrefix(name, "pages/"), ".html")
			if _, exists := pages[key]; exists {
				return fmt.Errorf("%s%s: page %q is defined already", prefix, name, key)
			}
			page, err := partials.Clone()
			if err != nil {
				return err
			}
			if _, err := page.New(prefix + name).Parse(src); err != nil {
				return err
			}
			pages[key] = page
			return resolved(page)
		})
	}
	if err := addPages(files, ""); err != nil {
		return nil, err
	}
	for _, bundle := range bundles {
		if err := addPages(bundle.Files, "addons/"+bundle.Name+"/"); err != nil {
			return nil, err
		}
	}
	return &renderer{partials: partials, pages: pages}, resolved(partials)
}

// resolved fails on a {{template}} naming nothing, which Go reports only when the call runs.
func resolved(set *template.Template) error {
	var tree *parse.Tree
	var walk func(parse.Node) error
	walk = func(n parse.Node) error {
		var branch *parse.BranchNode
		switch n := n.(type) {
		case *parse.ListNode:
			if n == nil {
				return nil
			}
			for _, c := range n.Nodes {
				if err := walk(c); err != nil {
					return err
				}
			}
		case *parse.IfNode:
			branch = &n.BranchNode
		case *parse.RangeNode:
			branch = &n.BranchNode
		case *parse.WithNode:
			branch = &n.BranchNode
		case *parse.TemplateNode:
			if set.Lookup(n.Name) == nil {
				at, _ := tree.ErrorContext(n)
				return fmt.Errorf("%s: no template %q", at, n.Name)
			}
		}
		if branch == nil {
			return nil
		}
		if err := walk(branch.List); err != nil {
			return err
		}
		return walk(branch.ElseList)
	}
	for _, t := range set.Templates() {
		if tree = t.Tree; tree != nil {
			if err := walk(tree.Root); err != nil {
				return err
			}
		}
	}
	return nil
}

// parseFile reads a template file and the names it defines.
func parseFile(files fs.FS, name string, funcs template.FuncMap) (string, []string, error) {
	src, err := fs.ReadFile(files, name)
	if err != nil {
		return "", nil, err
	}
	t, err := template.New(name).Funcs(funcs).Parse(string(src))
	if err != nil {
		return "", nil, err
	}
	var defined []string
	for _, d := range t.Templates() {
		if d.Name() != name {
			defined = append(defined, d.Name())
		}
	}
	return string(src), defined, nil
}

// Explained is one term and its meaning; a disclosure, since a phone has no hover.
type Explained struct {
	Term string
	Hint string
}

// RailRow is one place under an account in a section's rail.
type RailRow struct {
	Label  string
	Href   string
	Active bool
}

// templates are every template of every page, each once.
func (r *renderer) templates() []*template.Template {
	seen := map[*parse.Tree]bool{}
	var out []*template.Template
	for _, set := range append([]*template.Template{r.partials}, slices.Collect(maps.Values(r.pages))...) {
		for _, t := range set.Templates() {
			if t.Tree != nil && !seen[t.Tree] {
				seen[t.Tree] = true
				out = append(out, t)
			}
		}
	}
	return out
}
