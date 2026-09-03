package alborz

import (
	"slices"
	"time"
)

// themeVariants are the selectable stylesheet overlays; the value is
// validated here since it lands in a stylesheet URL.
var themeVariants = []string{"sublime", "glass", "ink"}

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
	ctx.SetCookie(ctx.cookie(name, value, preferenceCookieLife))
}

func (ctx *Context) pref(name string, valid func(string) bool) string {
	if v, ok := ctx.cookieValue(name, valid); ok {
		return v
	}
	return ""
}

// SetColorScheme stores the forced light or dark scheme per user.
func (ctx *Context) SetColorScheme(scheme string) {
	ctx.setPref(schemeCookieName, scheme, scheme == "light" || scheme == "dark")
}

// ColorScheme returns the user's forced scheme, empty for the system.
func (ctx *Context) ColorScheme() string {
	return ctx.pref(schemeCookieName, func(v string) bool { return v == "light" || v == "dark" })
}

// SetTheme stores the theme variant choice per user.
func (ctx *Context) SetTheme(theme string) {
	ctx.setPref(themeCookieName, theme, slices.Contains(themeVariants, theme))
}

// Theme returns the user's theme variant, empty for the default.
func (ctx *Context) Theme() string {
	return ctx.pref(themeCookieName, func(v string) bool { return slices.Contains(themeVariants, v) })
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

// secondaryCalendars are the calendar systems that can be shown beside
// the Gregorian one; empty shows none.
var secondaryCalendars = []string{"shcal"}

// displayKey holds the choices that follow the person rather than the
// browser, in the account's own store: which calendar someone counts in
// is not a property of the machine they read mail on.
const displayKey = "alborz.display"

type displayPrefs struct {
	Secondary string
}

// displayMemo keeps the store off the render path. The store does not
// remember a missing entry, so an unmade choice would otherwise cost a
// METADATA round trip on every page, behind the lock every IMAP command
// of the session queues on.
var displayMemo = NewMemo[displayPrefs](time.Hour)

func (ctx *Context) displayPrefs() displayPrefs {
	if ctx.Session == nil {
		return displayPrefs{}
	}
	prefs, err := displayMemo.Get(ctx.Session.Username(), func() (displayPrefs, error) {
		var p displayPrefs
		err := ctx.Session.Store().Get(displayKey, &p)
		if err == ErrNoStoreEntry {
			err = nil
		}
		return p, err
	})
	if err != nil {
		ctx.Logger().Printf("failed to read display preferences: %v", err)
	}
	return prefs
}

// SetSecondaryCalendar stores which calendar system is shown alongside
// the Gregorian dates.
func (ctx *Context) SetSecondaryCalendar(name string) error {
	if !slices.Contains(secondaryCalendars, name) {
		name = ""
	}
	prefs := ctx.displayPrefs()
	if prefs.Secondary == name {
		return nil
	}
	prefs.Secondary = name
	if err := ctx.Session.Store().Put(displayKey, &prefs); err != nil {
		return err
	}
	displayMemo.Forget(ctx.Session.Username())
	return nil
}

// SecondaryCalendar returns the chosen system, empty for none.
func (ctx *Context) SecondaryCalendar() string {
	return ctx.displayPrefs().Secondary
}

// SetLanguage stores the user's UI language choice in the browser,
// per user rather than per account; empty follows Accept-Language
// again.
func (ctx *Context) SetLanguage(code string) {
	if !IsLanguage(code) {
		code = ""
	}
	ctx.SetCookie(ctx.cookie(langCookieName, code, preferenceCookieLife))
}

// Language returns the user's explicit UI language choice, empty when
// following the browser preference.
func (ctx *Context) Language() string {
	if c, ok := ctx.cookieValue(langCookieName, IsLanguage); ok {
		return c
	}
	return ""
}

// SetUnified turns the merged all-accounts view on or off; it needs at
// least two signed-in accounts to turn on.
func (ctx *Context) SetUnified(on bool) {
	value := "1"
	if !on || len(ctx.accountSessions()) < 2 {
		value = ""
	}
	ctx.Unified = value != ""
	ctx.SetCookie(ctx.cookie(unifiedCookieName, value, 0))
}
