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

	Notice string

	// Build version, empty when the binary carries no VCS metadata
	Version string

	// User's timezone location for date formatting
	Timezone *time.Location

	// First day of week: 0=Sunday, 1=Monday (default), 6=Saturday
	FirstDayOfWeek int

	// additional plugin-specific data
	Extra map[string]interface{}
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

// Weekdays returns weekday names starting from FirstDayOfWeek.
func (g *GlobalRenderData) Weekdays() []string {
	names := []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	result := make([]string, 7)
	for i := 0; i < 7; i++ {
		result[i] = names[(g.FirstDayOfWeek+i)%7]
	}
	return result
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

	global := GlobalRenderData{
		Extra:          make(map[string]interface{}),
		Path:           strings.Split(ectx.Request().URL.Path, "/")[1:],
		Title:          "Webmail",
		URL:            ectx.Request().URL,
		FirstDayOfWeek: 1, // Monday default

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

func (brd *BaseRenderData) WithTitle(title string) *BaseRenderData {
	brd.GlobalData.Title = title
	return brd
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
	return r.theme.ExecuteTemplate(w, name, data)
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
