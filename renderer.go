package alborz

import (
	"fmt"
	"html/template"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"git.mehdix.org/alborz/shcal"
)

// GlobalRenderData contains data available in all templates.
type GlobalRenderData struct {
	Path []string
	URL  *url.URL

	LoggedIn bool

	// if logged in
	Username string
	Accounts []Account

	Title string

	HavePlugin func(name string) bool

	// Asset builds a theme asset's URL, version-stamped so the browser can
	// cache it; see Server.assetURL.
	Asset func(name string) string

	Notice string

	// Build version, empty when the binary carries no VCS metadata
	Version string
	// Year the page is served in, for the footer's line
	Year int
	// Secondary calendar system shown beside the Gregorian dates,
	// empty for none
	Secondary string

	// User's timezone location for date formatting
	Timezone *time.Location

	// First day of week: 0=Sunday, 1=Monday (default), 6=Saturday
	FirstDayOfWeek int

	// Unified marks the merged all-accounts view
	Unified bool

	// Account named by the request's account parameter, for links that
	// must keep pointing into the same account; empty otherwise.
	URLAccount string

	// Forced color scheme: "light" or "dark", empty to follow the system
	ColorScheme string

	// Theme variant stylesheet under assets/themes, empty for the default
	Theme string

	// UI language code: the user's cookie choice, else negotiated
	// from Accept-Language
	Lang string

	// additional plugin-specific data
	Extra map[string]interface{}
}

// Dir is the writing direction of the UI language.
func (g *GlobalRenderData) Dir() string {
	if g.Lang == "fa" {
		return "rtl"
	}
	return "ltr"
}

// BaseRenderData is the base type for templates. It should be extended with
// additional template-specific fields:
//
//	type MyRenderData struct {
//	    BaseRenderData
//	    // add additional fields here
//	}
type BaseRenderData struct {
	GlobalData GlobalRenderData
	// additional plugin-specific data
	Extra map[string]interface{}
}

// Global implements RenderData.
func (brd *BaseRenderData) Global() *GlobalRenderData {
	return &brd.GlobalData
}

// T translates a namespaced string key into the UI language.
func (brd *BaseRenderData) T(key string) string {
	return translate(brd.GlobalData.Lang, key)
}

// FormatDate formats a time in the user's timezone.
func (g *GlobalRenderData) FormatDate(t time.Time) string {
	if g.Timezone != nil {
		t = t.In(g.Timezone)
	}
	return t.Format("Mon Jan 02 15:04")
}

// FormatTime formats just the time portion in the user's timezone.
func (g *GlobalRenderData) FormatTime(t time.Time) string {
	if g.Timezone != nil {
		t = t.In(g.Timezone)
	}
	return t.Format("15:04")
}

// InTimezone converts a time to the user's timezone.
func (g *GlobalRenderData) InTimezone(t time.Time) time.Time {
	if g.Timezone != nil {
		return t.In(g.Timezone)
	}
	return t
}

// Weekdays returns translated weekday names starting from
// FirstDayOfWeek.
func (g *GlobalRenderData) Weekdays() []string {
	result := make([]string, 7)
	for i := 0; i < 7; i++ {
		result[i] = translate(g.Lang, dayKeys[(g.FirstDayOfWeek+i)%7])
	}
	return result
}

// WeekdayName translates the weekday of t.
func (g *GlobalRenderData) WeekdayName(t time.Time) string {
	return translate(g.Lang, dayKeys[int(t.Weekday())])
}

// MonthName translates the month of t.
func (g *GlobalRenderData) MonthName(t time.Time) string {
	return translate(g.Lang, monthKeys[t.Month()-1])
}

// SecondaryDay is the day number of the same date in the secondary
// calendar, empty when none is chosen. The first day of a month carries
// its month name, the way the Gregorian labels do.
func (g *GlobalRenderData) SecondaryDay(t time.Time) string {
	if g.Secondary != "shcal" {
		return ""
	}
	d := shcal.FromTime(g.InTimezone(t))
	if d.Day == 1 {
		return fmt.Sprintf("%d %s", d.Day, g.shMonthName(d.Month))
	}
	return fmt.Sprint(d.Day)
}

// SecondaryMonthYear names the secondary months a Gregorian month spans,
// empty when no secondary calendar is chosen.
func (g *GlobalRenderData) SecondaryMonthYear(t time.Time) string {
	if g.Secondary != "shcal" {
		return ""
	}
	t = g.InTimezone(t)
	first := shcal.FromTime(time.Date(t.Year(), t.Month(), 1, 12, 0, 0, 0, t.Location()))
	last := shcal.FromTime(time.Date(t.Year(), t.Month()+1, 0, 12, 0, 0, 0, t.Location()))
	if first.Month == last.Month && first.Year == last.Year {
		return fmt.Sprintf("%s %d", g.shMonthName(first.Month), first.Year)
	}
	if first.Year == last.Year {
		return fmt.Sprintf("%s – %s %d", g.shMonthName(first.Month), g.shMonthName(last.Month), last.Year)
	}
	return fmt.Sprintf("%s %d – %s %d", g.shMonthName(first.Month), first.Year,
		g.shMonthName(last.Month), last.Year)
}

// SecondaryDate is the full secondary date of one day.
func (g *GlobalRenderData) SecondaryDate(t time.Time) string {
	if g.Secondary != "shcal" {
		return ""
	}
	d := shcal.FromTime(g.InTimezone(t))
	return fmt.Sprintf("%d %s %d", d.Day, g.shMonthName(d.Month), d.Year)
}

func (g *GlobalRenderData) shMonthName(m shcal.Month) string {
	return translate(g.Lang, shMonthKeys[m-1])
}

// CalendarDayLabel keeps ordinary cells to a bare day number and repeats a
// compact month name only on the first day of each month.
func (g *GlobalRenderData) CalendarDayLabel(t time.Time) string {
	if t.Day() != 1 {
		return fmt.Sprint(t.Day())
	}
	monthRunes := []rune(g.MonthName(t))
	if len(monthRunes) > 3 {
		monthRunes = monthRunes[:3]
	}
	month := string(monthRunes)
	switch g.Lang {
	case "de":
		return fmt.Sprintf("%d. %s.", t.Day(), month)
	case "es", "fa":
		return fmt.Sprintf("%d %s", t.Day(), month)
	default:
		return fmt.Sprintf("%s %d", month, t.Day())
	}
}

// MonthYear renders the calendar heading for t's month.
func (g *GlobalRenderData) MonthYear(t time.Time) string {
	return fmt.Sprintf("%s %d", g.MonthName(t), t.Year())
}

// LongDate renders a translated day heading without relying on the
// process locale (time.Format only knows English names).
func (g *GlobalRenderData) LongDate(t time.Time) string {
	weekday := g.WeekdayName(t)
	month := g.MonthName(t)
	switch g.Lang {
	case "fa":
		return fmt.Sprintf("%s، %d %s %d", weekday, t.Day(), month, t.Year())
	case "de":
		return fmt.Sprintf("%s, %d. %s %d", weekday, t.Day(), month, t.Year())
	case "es":
		return fmt.Sprintf("%s, %d de %s de %d", weekday, t.Day(), month, t.Year())
	default:
		return fmt.Sprintf("%s, %s %d, %d", weekday, month, t.Day(), t.Year())
	}
}

// pluginEnabled reports whether the plugin applies to the request's
// session. The Enabled method is optional so that the Plugin interface
// stays unchanged for plugins serving every domain.
func pluginEnabled(p Plugin, ctx *Context) bool {
	e, ok := p.(interface{ Enabled(*Context) bool })
	return !ok || e.Enabled(ctx)
}

// RenderData is implemented by template data structs. It can be used to inject
// additional data to all templates.
type RenderData interface {
	// GlobalData returns a pointer to the global render data.
	Global() *GlobalRenderData
}

// NewBaseRenderData initializes a new BaseRenderData.
//
// It can be used by routes to pre-fill the base data:
//
//	type MyRenderData struct {
//	    BaseRenderData
//	    // add additional fields here
//	}
//
//	data := &MyRenderData{
//	    BaseRenderData: *alborz.NewBaseRenderData(ctx),
//	    // other fields...
//	}
func NewBaseRenderData(ectx echo.Context) *BaseRenderData {
	ctx, isactx := ectx.(*Context)

	lang := requestLanguage(ectx)
	global := GlobalRenderData{
		Extra:          make(map[string]interface{}),
		Path:           strings.Split(ectx.Request().URL.Path, "/")[1:],
		Title:          brandName,
		URL:            ectx.Request().URL,
		FirstDayOfWeek: 1, // Monday default
		Lang:           lang,

		Asset: func(name string) string {
			if !isactx {
				return "/assets/" + name
			}
			return ctx.Server.assetURL(name)
		},

		HavePlugin: func(name string) bool {
			if !isactx {
				return false
			}
			for _, plugin := range ctx.Server.plugins {
				if plugin.Name() == name {
					return pluginEnabled(plugin, ctx)
				}
			}
			return false
		},
	}

	if isactx {
		global.Version = ctx.Server.Options.Version
		global.Year = time.Now().Year()
		global.Secondary = ctx.SecondaryCalendar()
		global.ColorScheme = ctx.ColorScheme()
		global.Theme = ctx.Theme()
	}

	if isactx {
		global.Unified = ctx.Unified
		global.URLAccount = ctx.urlAccount
	}

	if isactx && ctx.Session != nil {
		global.LoggedIn = true
		global.Username = ctx.Session.username
		global.Accounts = ctx.Accounts()
		global.Notice = ctx.Session.PopNotice()
	}

	return &BaseRenderData{
		GlobalData: global,
		Extra:      make(map[string]interface{}),
	}
}

// T translates a namespaced string key into the request's UI
// language, for strings built on the Go side.
func (ctx *Context) T(key string) string {
	return translate(requestLanguage(ctx), key)
}

// requestLanguage resolves the request's UI language: the cookie
// choice wins, else the Accept-Language negotiation.
func requestLanguage(ectx echo.Context) string {
	if c, err := ectx.Cookie(langCookieName); err == nil && IsLanguage(c.Value) {
		return c.Value
	}
	return MatchLanguage(ectx.Request().Header.Get("Accept-Language"))
}

// RenderInfo renders a full page carrying one explanatory sentence,
// for valid routes whose answer is a state, not content: an
// unconfigured section, a message that does not exist.
func RenderInfo(ctx *Context, code int, message string) error {
	data := struct {
		BaseRenderData
		Message string
	}{*NewBaseRenderData(ctx), message}
	return ctx.Render(code, "info.html", &data)
}

// WithTitle sets the page's own title; the brand is appended when
// the template renders it, so every page carries it.
func (brd *BaseRenderData) WithTitle(title string) *BaseRenderData {
	brd.GlobalData.Title = title
	return brd
}

// MonthYearIn translates a month heading for the request's language,
// for titles built before the render data exists.
func (ctx *Context) MonthYearIn(t time.Time) string {
	lang := requestLanguage(ctx)
	return fmt.Sprintf("%s %d", translate(lang, monthKeys[t.Month()-1]), t.Year())
}

// PageTitle is the browser title: the page's subject and the brand,
// or the brand alone on pages that name nothing.
func (g *GlobalRenderData) PageTitle() string {
	if g.Title == "" || g.Title == brandName {
		return brandName
	}
	return g.Title + " - " + brandName
}

type renderer struct {
	logger       echo.Logger
	themesPath   string
	defaultTheme string

	theme *template.Template
}

func (r *renderer) Render(w io.Writer, name string, data interface{}, ectx echo.Context) error {
	// ectx is the raw *echo.context, not our own *Context
	ctx := ectx.Get("context").(*Context)

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

	for _, plugin := range ctx.Server.plugins {
		if !pluginEnabled(plugin, ctx) {
			continue
		}
		if err := plugin.Inject(ctx, name, renderData); err != nil {
			return fmt.Errorf("failed to run plugin %q: %v", plugin.Name(), err)
		}
	}

	// TODO: per-user theme selection
	start := time.Now()
	err := r.theme.ExecuteTemplate(w, name, data)
	ctx.timing.add("render", start, time.Now())
	return err
}

// loadTheme parses the embedded theme, then overlays the same-named theme
// directory on disk, so a theme only carries the files it changes.
func loadTheme(themesPath string, name string, base *template.Template) (*template.Template, int, error) {
	theme, err := base.Clone()
	if err != nil {
		return nil, 0, err
	}

	theme, err = theme.ParseFS(embeddedTheme, "themes/alborz/*.html")
	if err != nil {
		return nil, 0, err
	}

	overlays, err := filepath.Glob(themesPath + "/" + name + "/*.html")
	if err != nil {
		return nil, 0, err
	}
	if len(overlays) > 0 {
		if theme, err = theme.ParseFiles(overlays...); err != nil {
			return nil, 0, err
		}
	}

	return theme, len(overlays), nil
}

func (r *renderer) Load(plugins []Plugin) error {
	base := template.New("")

	for _, p := range plugins {
		if err := p.LoadTemplate(base); err != nil {
			return fmt.Errorf("failed to load template for plugin %q: %v", p.Name(), err)
		}
	}

	theme, overlays, err := loadTheme(r.themesPath, r.defaultTheme, base)
	if err != nil {
		return fmt.Errorf("failed to load theme %q: %v", r.defaultTheme, err)
	}
	r.logger.Printf("Loaded theme %q, %d files overridden on disk", r.defaultTheme, overlays)

	r.theme = theme
	return nil
}

func newRenderer(logger echo.Logger, themesPath string, defaultTheme string) *renderer {
	return &renderer{
		logger:       logger,
		defaultTheme: defaultTheme,
		themesPath:   themesPath,
	}
}
