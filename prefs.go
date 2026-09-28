package alborz

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// readThemes are the stylesheet overlays on offer: the ones built into
// the binary and any a deployment dropped into the theme's assets
// beside them, which is how somebody's own palette becomes a choice in
// the settings without a rebuild. The value lands in a stylesheet URL,
// so what is not in this list is not accepted.
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

// Theme is one overlay on offer. Scheme is the one it pins - a theme
// may be written for the light side only, or the dark - and empty where
// it answers to both, which is what decides whether the scheme toggle
// has anything to do on it.
type Theme struct {
	Name   string
	Scheme string
}

// pinnedScheme reads a theme's stylesheet for the scheme it is written
// in. A sheet with a dark block answers to both sides; one without is
// the scheme it declares, whatever the system says.
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

// cookieProfile is the browser's own copy of the display, for a page
// rendered with no account signed in; the rest is the default.
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

// setProfileCookies keeps the browser's copy in step with the profile.
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

// ThemeScheme is the scheme the chosen theme pins, empty where it
// follows the reader's own choice.
func (ctx *Context) ThemeScheme() string {
	name := ctx.Theme()
	for _, t := range ctx.Server.themes {
		if t.Name == name {
			return t.Scheme
		}
	}
	return ""
}

// Theme returns the user's theme variant, empty for the default or one
// this deployment does not offer.
func (ctx *Context) Theme() string {
	if theme := ctx.Profile().Display.Theme; ctx.Server.hasTheme(theme) {
		return theme
	}
	return ""
}

// AlignByScript reports whether lines align by their own script
// rather than with the interface's edge.
func (ctx *Context) AlignByScript() bool {
	return ctx.Profile().Display.AlignByScript
}

// textSizes are the reading sizes on offer beside the default. Every
// font size in the stylesheet is already relative, so one declaration
// on the root scales the whole interface.
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

// Calendars returns the chosen systems: the one counted in, empty for
// Gregorian, and the one glossed, empty for none.
func (ctx *Context) Calendars() (primary, secondary string) {
	r := ctx.Reading()
	return r.Primary, r.Secondary
}

// CalendarSystem is the system the reader counts in.
func (ctx *Context) CalendarSystem() CalendarSystem {
	return Calendar(ctx.Reading().Primary)
}

// SetLanguage puts a UI language in the profile; empty follows
// Accept-Language again.
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

// Language returns the user's explicit UI language choice, empty when
// following the browser preference.
func (ctx *Context) Language() string {
	if code := ctx.Profile().Display.Language; IsLanguage(code) {
		return code
	}
	return ""
}
