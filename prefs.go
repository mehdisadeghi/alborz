package alborz

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The value lands in a stylesheet URL, so only listed themes are accepted.
func readThemes(themesPath, theme string) []Theme {
	found := map[string]Theme{}
	add := func(name string, source []byte) {
		short := strings.TrimSuffix(filepath.Base(name), ".css")
		if short == "" {
			return
		}
		found[short] = Theme{Name: short, Scheme: pinnedScheme(string(source))}
	}
	built, _ := fs.Glob(embeddedTheme, "themes/alborz/assets/themes/*.css")
	for _, name := range built {
		source, _ := fs.ReadFile(embeddedTheme, name)
		add(name, source)
	}
	onDisk, _ := filepath.Glob(filepath.Join(themesPath, theme, "assets", "themes", "*.css"))
	for _, name := range onDisk {
		source, _ := os.ReadFile(name)
		add(name, source)
	}
	themes := make([]Theme, 0, len(found))
	for _, t := range found {
		themes = append(themes, t)
	}
	slices.SortFunc(themes, func(a, b Theme) int { return strings.Compare(a.Name, b.Name) })
	return themes
}

// Theme is one overlay on offer; Scheme is the scheme it pins, empty when it answers to both.
type Theme struct {
	Name   string
	Scheme string
}

func pinnedScheme(source string) string {
	if strings.Contains(source, "prefers-color-scheme: dark") || strings.Contains(source, `[data-theme="dark"]`) {
		return ""
	}
	if _, rest, ok := strings.Cut(source, "color-scheme:"); ok {
		name, _, _ := strings.Cut(strings.TrimSpace(rest), ";")
		name = strings.TrimSpace(name)
		if name == "light" || name == "dark" {
			return name
		}
	}
	return ""
}

// A browser may send several cookies of one name, and http.Request.Cookie returns only the first.
func (ctx *Context) cookieValues(name string) []string {
	var values []string
	for _, c := range ctx.Request().Cookies() {
		if c.Name == name {
			values = append(values, c.Value)
		}
	}
	return values
}

// Skipping invalid values keeps a stale duplicate from masking a usable one.
func (ctx *Context) cookieValue(name string, valid func(string) bool) (string, bool) {
	for _, v := range ctx.cookieValues(name) {
		if valid(v) {
			return v, true
		}
	}
	return "", false
}

// cookieProfile serves pages rendered with no account signed in.
func (ctx *Context) cookieProfile() Profile {
	has := func(name string) string {
		v, _ := ctx.cookieValue(name, func(v string) bool { return v != "" })
		return v
	}
	return Profile{
		Reading: Reading{MessagesPerPage: DefaultMessagesPerPage},
		Display: Display{
			Theme:         has(themeCookieName),
			TextSize:      has(textSizeCookieName),
			AlignByScript: has(alignCookieName) == "1",
			Language:      has(langCookieName),
		},
	}
}

func (ctx *Context) setProfileCookies(d Display) {
	align := ""
	if d.AlignByScript {
		align = "1"
	}
	for name, value := range map[string]string{themeCookieName: d.Theme, textSizeCookieName: d.TextSize,
		alignCookieName: align, langCookieName: d.Language} {
		ctx.SetCookie(ctx.cookie(name, value, preferenceCookieLife))
	}
}

// Themes are the overlays this deployment offers.
func (ctx *Context) Themes() []Theme {
	return ctx.Server.themes
}

// ThemeScheme is the scheme the chosen theme pins, empty when it follows the reader.
func (ctx *Context) ThemeScheme() string {
	name := ctx.Theme()
	for _, t := range ctx.Server.themes {
		if t.Name == name {
			return t.Scheme
		}
	}
	return ""
}

// Theme returns the user's theme, empty for the default or one not offered.
func (ctx *Context) Theme() string {
	if theme := ctx.Profile().Display.Theme; ctx.Server.hasTheme(theme) {
		return theme
	}
	return ""
}

// AlignByScript reports whether lines align by their own script rather than the interface's edge.
func (ctx *Context) AlignByScript() bool {
	return ctx.Profile().Display.AlignByScript
}

var textSizes = []string{"large", "larger"}

// TextSizes are the sizes on offer beside the default.
func TextSizes() []string { return textSizes }

// TextSize returns the chosen size, empty for the default.
func (ctx *Context) TextSize() string {
	if size := ctx.Profile().Display.TextSize; slices.Contains(textSizes, size) {
		return size
	}
	return ""
}

// Calendars returns the primary system, empty for Gregorian, and the secondary, empty for none.
func (ctx *Context) Calendars() (primary, secondary string) {
	r := ctx.Reading()
	return r.Primary, r.Secondary
}

// CalendarSystem is the system the reader counts in.
func (ctx *Context) CalendarSystem() CalendarSystem {
	return Calendar(ctx.Reading().Primary)
}

// SetLanguage sets the UI language; empty follows Accept-Language.
func (ctx *Context) SetLanguage(code string) error {
	if !IsLanguage(code) {
		code = ""
	}
	p := ctx.Profile()
	if p.Display.Language == code {
		return nil
	}
	p.Display.Language = code
	return ctx.SetProfile(p)
}

// Language returns the explicit UI language, empty when following the browser.
func (ctx *Context) Language() string {
	if code := ctx.Profile().Display.Language; IsLanguage(code) {
		return code
	}
	return ""
}
