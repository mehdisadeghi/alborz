package web

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/calsys"
	"git.mehdix.org/alborz/internal/locale"
)

// The value lands in a stylesheet URL, so only listed themes are accepted.
func readThemes(files fs.FS) (base Theme, themes []Theme, err error) {
	names, _ := fs.Glob(files, "assets/themes/*.css")
	base, err = readTheme(files, "assets/style.css")
	for _, name := range names {
		t, terr := readTheme(files, name)
		themes = append(themes, t)
		err = errors.Join(err, terr)
	}
	return base, themes, err
}

func readTheme(files fs.FS, name string) (Theme, error) {
	source, err := fs.ReadFile(files, name)
	if err != nil {
		return Theme{}, err
	}
	t := Theme{Scheme: pinnedScheme(string(source))}
	if path.Dir(name) == "assets/themes" {
		t.Name = strings.TrimSuffix(path.Base(name), ".css")
	}
	colors := themeColors(string(source))
	switch {
	case t.Scheme != "" && len(colors) == 1:
		t.Colors = []ThemeColor{{Color: colors[0]}}
	case t.Scheme == "" && len(colors) >= 2:
		t.Colors = []ThemeColor{{Scheme: "light", Color: colors[0]}, {Scheme: "dark", Color: colors[len(colors)-1]}}
	default:
		return t, fmt.Errorf("%s: %d %s declarations; a theme pinning a scheme needs one, any other a light and a dark", name, len(colors), themeColorToken)
	}
	return t, nil
}

// Theme is one overlay on offer; Scheme is the scheme it pins, empty when it answers to both.
type Theme struct {
	Name   string
	Scheme string
	// Colors are what the browser paints around the window, per scheme; one with no scheme for a pinned theme.
	Colors []ThemeColor
}

// ThemeColor is a theme's colour for the browser's chrome in one scheme, empty for every scheme.
type ThemeColor struct {
	Scheme string
	Color  string
}

// The token names what it feeds: <meta name="theme-color">.
const themeColorToken = "--theme-color:"

// themeColors are the token's values in source order: the light scheme's first, the dark scheme's last.
func themeColors(source string) []string {
	var colors []string
	for _, rest := range strings.Split(source, themeColorToken)[1:] {
		color, _, _ := strings.Cut(rest, ";")
		colors = append(colors, strings.TrimSpace(color))
	}
	return colors
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
func (ctx *Context) cookieProfile() alborz.Profile {
	has := func(name string) string {
		v, _ := ctx.cookieValue(name, func(v string) bool { return v != "" })
		return v
	}
	return alborz.Profile{
		Reading: alborz.Reading{MessagesPerPage: alborz.DefaultMessagesPerPage},
		Display: alborz.Display{
			Theme:         has(themeCookieName),
			TextSize:      has(textSizeCookieName),
			AlignByScript: has(alignCookieName) == "1",
			Language:      has(langCookieName),
		},
	}
}

func (ctx *Context) setProfileCookies(d alborz.Display) {
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

// theme is the chosen overlay, or the base stylesheet when none is.
func (ctx *Context) theme() Theme {
	name := ctx.Theme()
	for _, t := range ctx.Server.themes {
		if t.Name == name {
			return t
		}
	}
	return ctx.Server.base
}

// Theme returns the user's theme, empty for the default or one not offered.
func (ctx *Context) Theme() string {
	if theme := ctx.Profile().Display.Theme; ctx.Server.hasTheme(theme) {
		return theme
	}
	return ""
}

// alignByScript reports whether lines align by their own script rather than the interface's edge.
func (ctx *Context) alignByScript() bool {
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

// calendars returns the primary system, empty for Gregorian, and the secondary, empty for none.
func (ctx *Context) calendars() (primary, secondary string) {
	r := ctx.Reading()
	return r.Primary, r.Secondary
}

// CalendarSystem is the system the reader counts in.
func (ctx *Context) CalendarSystem() calsys.System {
	return calsys.Of(ctx.Reading().Primary)
}

// SetLanguage sets the UI language; empty follows Accept-Language.
func (ctx *Context) SetLanguage(code string) error {
	if !locale.Supported(code) {
		code = ""
	}
	p := ctx.Profile()
	if p.Display.Language == code {
		return nil
	}
	p.Display.Language = code
	return ctx.SetProfile(p)
}

// language returns the explicit UI language, empty when following the browser.
func (ctx *Context) language() string {
	if code := ctx.Profile().Display.Language; locale.Supported(code) {
		return code
	}
	return ""
}

// langCookieName holds the language in the browser, since it is per person, not per account.
const langCookieName = "alborz_lang"
