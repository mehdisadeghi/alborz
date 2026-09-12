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

// setPref stores a per-user display preference in the browser; empty
// clears it back to the default.
func (ctx *Context) setPref(name, value string, valid bool) {
	if !valid {
		value = ""
	}
	ctx.keepPref(name, value)
	ctx.SetCookie(ctx.cookie(name, value, preferenceCookieLife))
}

// keepPref remembers a choice for the rest of this request, so a
// handler that answers with the thing it just changed shows the change
// rather than what the browser sent.
func (ctx *Context) keepPref(name, value string) {
	if ctx.prefs == nil {
		ctx.prefs = map[string]string{}
	}
	ctx.prefs[name] = value
}

func (ctx *Context) pref(name string, valid func(string) bool) string {
	if v, ok := ctx.prefs[name]; ok {
		if v == "" || valid(v) {
			return v
		}
		return ""
	}
	if v, ok := ctx.cookieValue(name, valid); ok {
		return v
	}
	return ""
}

// SetColorScheme forces light or dark, or follows the system when the
// scheme is neither. It lasts as long as the browser is open and no
// longer, and it is never written down anywhere but this browser: a
// scheme forced against the system's is an answer to the light in the
// room, and the light in the room changes.
func (ctx *Context) SetColorScheme(scheme string) {
	if scheme != "light" && scheme != "dark" {
		scheme = ""
	}
	ctx.keepPref(schemeCookieName, scheme)
	ctx.SetCookie(ctx.cookie(schemeCookieName, scheme, 0))
}

// ColorScheme returns the user's forced scheme, empty for the system.
func (ctx *Context) ColorScheme() string {
	return ctx.pref(schemeCookieName, func(v string) bool { return v == "light" || v == "dark" })
}

// SetTheme stores the theme variant choice per user.
func (ctx *Context) SetTheme(theme string) {
	ctx.setPref(themeCookieName, theme, ctx.Server.hasTheme(theme))
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

// Theme returns the user's theme variant, empty for the default.
func (ctx *Context) Theme() string {
	return ctx.pref(themeCookieName, func(v string) bool { return ctx.Server.hasTheme(v) })
}

// SetAccountColors stores whether merged lists mark each row with its
// account's color. It is a reading aid of one browser, not a property
// of the accounts, so it stays out of their stores.
func (ctx *Context) SetAccountColors(on bool) {
	value := ""
	if on {
		value = "1"
	}
	ctx.setPref(accountColorsCookieName, value, on)
}

// AccountColors reports whether the color marks are switched on.
func (ctx *Context) AccountColors() bool {
	return ctx.pref(accountColorsCookieName, func(v string) bool { return v == "1" }) == "1"
}

// SetAlignByScript stores whether a line aligns by its own script
// rather than with the interface's edge.
func (ctx *Context) SetAlignByScript(on bool) {
	value := ""
	if on {
		value = "1"
	}
	ctx.setPref(alignCookieName, value, on)
}

// AlignByScript reports whether lines align by their own script.
func (ctx *Context) AlignByScript() bool {
	return ctx.pref(alignCookieName, func(v string) bool { return v == "1" }) == "1"
}

// textSizes are the reading sizes on offer beside the default. Every
// font size in the stylesheet is already relative, so one declaration
// on the root scales the whole interface.
var textSizes = []string{"large", "larger"}

// SetTextSize stores the reader's chosen text size. It is a property of
// the eyes reading the page rather than of any account, so it lives in
// the browser beside the theme and the language.
func (ctx *Context) SetTextSize(size string) {
	ctx.setPref(textSizeCookieName, size, slices.Contains(textSizes, size))
}

// TextSize returns the chosen size, empty for the default.
func (ctx *Context) TextSize() string {
	return ctx.pref(textSizeCookieName, func(v string) bool { return slices.Contains(textSizes, v) })
}

// Calendars returns the chosen systems: the one counted in, empty for
// Gregorian, and the one glossed, empty for none. They are the
// reader's, not an account's: which calendar someone counts in is not
// a property of any one mailbox.
func (ctx *Context) Calendars() (primary, secondary string) {
	r := ctx.Reading()
	return r.Primary, r.Secondary
}

// CalendarSystem is the system the reader counts in.
func (ctx *Context) CalendarSystem() CalendarSystem {
	return Calendar(ctx.Reading().Primary)
}

// SetLanguage stores the user's UI language choice in the browser,
// per user rather than per account; empty follows Accept-Language
// again.
func (ctx *Context) SetLanguage(code string) {
	if !IsLanguage(code) {
		code = ""
	}
	ctx.keepPref(langCookieName, code)
	ctx.SetCookie(ctx.cookie(langCookieName, code, preferenceCookieLife))
}

// Language returns the user's explicit UI language choice, empty when
// following the browser preference.
func (ctx *Context) Language() string {
	return ctx.pref(langCookieName, IsLanguage)
}
