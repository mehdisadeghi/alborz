package alborz

import (
	"fmt"
	"html/template"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/dromara/carbon/v2"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"
	"golang.org/x/text/unicode/bidi"
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

	Notice *Notice

	// Build version, empty when the binary carries no VCS metadata
	Version string
	// Brand is the product's name as a person reads it, so no page has
	// to spell it and none can spell it differently.
	Brand string
	// ProjectURL is where the footer's name links, when a deployment
	// names one. Empty prints the name as text.
	ProjectURL string
	// Language is the explicit choice, empty while following the
	// browser; LanguageChoices are what the menu offers.
	Language        string
	LanguageChoices []LanguageChoice
	// Year the page is served in, for the footer's line
	Year int
	// Primary is the calendar system the page counts in, Secondary the
	// one glossed beside it, empty for none.
	Primary   string
	Secondary string

	// User's timezone location for date formatting
	Timezone *time.Location

	// First day of week: 0=Sunday, 1=Monday (default), 6=Saturday
	FirstDayOfWeek int

	// Unified marks the merged all-accounts view
	Unified bool

	// AccountColors marks merged rows with a per-account color, an
	// opt-in reading aid on top of the account's name
	AccountColors bool

	// AlignByScript lets each line align by its own writing direction
	// instead of with the interface's edge
	AlignByScript bool

	// Account named by the request's account parameter, for links that
	// must keep pointing into the same account; empty otherwise.
	URLAccount string

	// Forced color scheme: "light" or "dark", empty to follow the system
	ColorScheme string

	// Theme variant stylesheet under assets/themes, empty for the default
	Theme string

	// TextSize scales the whole interface for a reader who wants it
	// larger, empty for the size everyone else gets
	TextSize string

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

// carbonLangs holds one prepared carbon language per UI language. They
// are built once and only read afterwards, which is what makes sharing
// them across requests safe.
var carbonLangs = map[string]*carbon.Language{}

// carbonFixes correct what carbon's own locale files get wrong.
var carbonFixes = map[string]map[string]string{
	// German says "vor" and "in" with the dative, which takes -n in the
	// plural; carbon ships "vor 3 Tage".
	"de": {
		"year":  "1 Jahr|%d Jahren",
		"month": "1 Monat|%d Monaten",
		"day":   "1 Tag|%d Tagen",
	},
	// Spanish writes its months and weekdays in lower case, and keeps
	// their accents.
	"es": {
		"months":       "enero|febrero|marzo|abril|mayo|junio|julio|agosto|septiembre|octubre|noviembre|diciembre",
		"short_months": "ene|feb|mar|abr|may|jun|jul|ago|sep|oct|nov|dic",
		"weeks":        "domingo|lunes|martes|miércoles|jueves|viernes|sábado",
		"short_weeks":  "dom|lun|mar|mié|jue|vie|sáb",
	},
}

func init() {
	for _, code := range languages {
		lang := carbon.NewLanguage().SetLocale(code)
		if fixes, ok := carbonFixes[code]; ok {
			lang.SetResources(fixes)
		}
		if lang.Error != nil {
			panic(fmt.Sprintf("alborz: carbon has no usable %q locale: %v", code, lang.Error))
		}
		carbonLangs[code] = lang
	}
}

// at reads t in the user's zone under the page's language, so every
// name carbon spells comes back translated.
func (g *GlobalRenderData) at(t time.Time) *carbon.Carbon {
	lang, ok := carbonLangs[g.Lang]
	if !ok {
		lang = carbonLangs["en"]
	}
	return carbon.CreateFromStdTime(g.InTimezone(t)).SetLanguage(lang)
}

// Since dates a message the way a list reads it, in the reader's
// language: "5 minutes ago", "vor 3 Tagen", "۸ ماه پیش". The exact date
// belongs in the row's tooltip, where precision costs no width.
func (g *GlobalRenderData) Since(t time.Time) string {
	return g.shapeDigits(g.at(t).DiffForHumans())
}

// FormatDate states a full date and time: a message header and an event
// page have the room to spell out what a list column abbreviates.
func (g *GlobalRenderData) FormatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return fmt.Sprintf("%s%s %s", g.LongDate(t), g.comma(), g.FormatTime(t))
}

// comma is the list separator of the page's script.
func (g *GlobalRenderData) comma() string {
	if g.Lang == "fa" {
		return "،"
	}
	return ","
}

// ShortDate is a bare date, counted in the secondary calendar when one
// is chosen: a reader who asked to count in Solar Hijri counts there
// wherever a single date fits. The year is left off within this one.
func (g *GlobalRenderData) ShortDate(t time.Time) string {
	cal := g.primary()
	year, m, day := cal.Date(g.InTimezone(t))
	thisYear, _, _ := cal.Date(g.InTimezone(time.Now()))
	month := g.shortMonth(cal, m)
	p := g.printer()
	var s, yearSep string
	switch g.Lang {
	case "de":
		s, yearSep = p.Sprintf("%d. %s", day, month), " "
	case "es", "fa":
		s, yearSep = p.Sprintf("%d %s", day, month), " "
	default:
		// Only English separates the day from the year with a comma.
		s, yearSep = p.Sprintf("%s %d", month, day), ", "
	}
	if year != thisYear {
		s = s + yearSep + g.year(year)
	}
	return s
}

// FormatTime formats just the time portion in the user's timezone.
func (g *GlobalRenderData) FormatTime(t time.Time) string {
	t = g.InTimezone(t)
	return g.printer().Sprintf("%02d:%02d", t.Hour(), t.Minute())
}

// InTimezone converts a time to the user's timezone.
func (g *GlobalRenderData) InTimezone(t time.Time) time.Time {
	if g.Timezone != nil {
		return t.In(g.Timezone)
	}
	return t
}

// weekOne is a Sunday, so that adding a weekday index to it names that
// weekday. Any Sunday would do; this one is arbitrary.
var weekOne = time.Date(2023, time.January, 1, 12, 0, 0, 0, time.UTC)

// Weekdays returns translated weekday names starting from
// FirstDayOfWeek.
func (g *GlobalRenderData) Weekdays() []string {
	result := make([]string, 7)
	for i := range result {
		result[i] = g.WeekdayName(weekOne.AddDate(0, 0, (g.FirstDayOfWeek+i)%7))
	}
	return result
}

// WeekdayName translates the weekday of t.
func (g *GlobalRenderData) WeekdayName(t time.Time) string {
	return g.at(t).ToWeekString()
}

// MonthYearShort is MonthYear abbreviated, for a toolbar too narrow to
// carry the month's full name beside its controls.
func (g *GlobalRenderData) MonthYearShort(t time.Time) string {
	cal := g.primary()
	y, m, _ := cal.Date(g.InTimezone(t))
	return fmt.Sprintf("%s %s", g.shortMonth(cal, m), g.year(y))
}

// MonthName translates the month of t, in the primary calendar.
func (g *GlobalRenderData) MonthName(t time.Time) string {
	cal := g.primary()
	_, m, _ := cal.Date(g.InTimezone(t))
	return cal.MonthName(m, g.Lang)
}

// shortMonth abbreviates a Gregorian month the way the language does;
// Solar Hijri months have no short form anyone would recognise.
func (g *GlobalRenderData) shortMonth(cal CalendarSystem, month int) string {
	if cal.Name() != gregorianName {
		return cal.MonthName(month, g.Lang)
	}
	short := g.at(time.Date(2000, time.Month(month), 1, 12, 0, 0, 0, time.UTC)).ToShortMonthString()
	if g.Lang == "de" {
		// German marks an abbreviation with a period.
		short += "."
	}
	return short
}

// primary is the calendar system the page counts in, and secondary the
// one glossed beside it.
func (g *GlobalRenderData) primary() CalendarSystem { return Calendar(g.Primary) }

func (g *GlobalRenderData) secondary() (CalendarSystem, bool) {
	if g.Secondary == "" || g.Secondary == g.primary().Name() {
		return nil, false
	}
	return Calendar(g.Secondary), true
}

// Day is the day number of t in the primary calendar.
func (g *GlobalRenderData) Day(t time.Time) string {
	_, _, d := g.primary().Date(g.InTimezone(t))
	return g.Num(d)
}

// SameMonth says whether two instants fall in one month of the primary
// calendar, which is what the grid dims by and the agenda filters on.
func (g *GlobalRenderData) SameMonth(a, b time.Time) bool {
	cal := g.primary()
	ay, am, _ := cal.Date(g.InTimezone(a))
	by, bm, _ := cal.Date(g.InTimezone(b))
	return ay == by && am == bm
}

// replyPrefix matches the marks a mail client puts in front of a
// subject when it answers or forwards one, in the languages alborz
// speaks plus the ones its lists carry, and a list's [tag].
var replyPrefix = regexp.MustCompile(`^\s*(?i:(re|aw|sv|antw|fw|fwd|rv|tr)\s*(\[\d+\])?\s*:|\[[^\]]{1,40}\])\s*`)

// SubjectDir is the writing direction a subject should be read in.
//
// dir=auto and the bidi algorithm both take the first strong character,
// which a reply prefix supplies: "Re: درخواست راهنمایی" is a Persian
// subject that renders left to right because two Latin letters happen
// to lead it. The prefixes are stripped first, so the direction comes
// from what the subject actually says; x/text then applies the same
// rule to the remainder.
func SubjectDir(subject string) string {
	stripped := subject
	for {
		trimmed := replyPrefix.ReplaceAllString(stripped, "")
		if trimmed == stripped {
			break
		}
		stripped = trimmed
	}
	if strings.TrimSpace(stripped) == "" {
		stripped = subject
	}
	if strings.TrimSpace(stripped) == "" {
		return "auto"
	}
	// Order must run before the direction is asked for: x/text panics
	// on a paragraph it has not ordered yet.
	var p bidi.Paragraph
	if _, err := p.SetString(stripped); err != nil {
		return "auto"
	}
	if _, err := p.Order(); err != nil {
		return "auto"
	}
	if p.IsLeftToRight() {
		return "ltr"
	}
	return "rtl"
}

// ContentLang names the language of text the interface shows, relative
// to the page it is shown on: empty where the two agree, so the
// attribute appears only where it changes the voice.
func (g *GlobalRenderData) ContentLang(s string) string {
	return ContentLang(s, g.Lang)
}

// MessageLang is ContentLang for what a message itself says.
func (g *GlobalRenderData) MessageLang(s string) string {
	return MessageLang(s, g.Lang)
}

// ShortAccount names an account as briefly as it can still be told
// apart: the domain alone while it is the only account signed in there,
// the whole address once two accounts share one domain.
func ShortAccount(account string, accounts []Account) string {
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
	return ShortAccount(account, g.Accounts)
}

// AccountTrack is the width the account label's track holds: the widest
// label of the accounts signed in. Sized to its own text the label
// starts wherever a name happens to end, so a column of them is ragged
// on the side that faces the sender.
func (g GlobalRenderData) AccountTrack() template.CSS {
	n := 0
	for _, a := range g.Accounts {
		if l := len([]rune(g.AccountLabel(a.Username))); l > n {
			n = l
		}
	}
	return template.CSS(fmt.Sprintf("%dch", n))
}

// AccountColor is the mark a merged row wears in the opt-in color mode.
// The hues are spread over the accounts actually signed in, in name
// order, so no two are a shade apart: hashing each name on its own gave
// two of three accounts 25 and 27 degrees, which reads as one color.
// The mark is a reading aid layered on the account's name, never the
// only thing that says whose a row is.
func (g GlobalRenderData) AccountColor(account string) template.CSS {
	names := make([]string, len(g.Accounts))
	for i, a := range g.Accounts {
		names[i] = a.Username
	}
	slices.Sort(names)
	i := slices.Index(names, account)
	if i < 0 {
		return ""
	}
	return template.CSS(fmt.Sprintf("hsl(%d 60%% 45%%)", i*360/len(names)))
}

// printers write numbers the way each language writes them - Persian
// digits on a Persian page, each locale's own grouping - out of CLDR's
// tables in x/text. Nothing here knows what a digit looks like.
var printers = map[string]*message.Printer{}

func init() {
	for _, code := range languages {
		printers[code] = message.NewPrinter(language.MustParse(code))
	}
}

// The receivers below are values, not pointers: templates hand the
// global to a define inside a tuple, which boxes it and leaves pointer
// methods out of reach.
func (g GlobalRenderData) printer() *message.Printer {
	if p, ok := printers[g.Lang]; ok {
		return p
	}
	return printers["en"]
}

// Num writes a count as the page's language writes one, grouped:
// 12345 is "12,345", "12.345" or "۱۲٬۳۴۵".
func (g GlobalRenderData) Num(n int) string {
	return g.printer().Sprintf("%d", n)
}

// year writes a year, which no language groups: 2026 is never "2,026".
func (g GlobalRenderData) year(n int) string {
	return g.printer().Sprint(number.Decimal(n, number.NoSeparator()))
}

// Tf translates a format string and fills it in the page's language, so
// the numbers inside a sentence are written like the sentence.
func (g GlobalRenderData) Tf(key string, args ...interface{}) string {
	count := 1
	if len(args) > 0 {
		if n, ok := args[0].(int); ok {
			count = n
		}
	}
	return g.printer().Sprintf(translateCount(g.Lang, key, count), args...)
}

// persianDigits shapes the digits inside a string a library already
// formatted. It exists only for carbon, which fills its own relative
// phrases ("%d ماه پیش") with Latin numerals and offers no hook: every
// number alborz formats itself goes through a printer instead.
var persianDigits = [10]rune{'۰', '۱', '۲', '۳', '۴', '۵', '۶', '۷', '۸', '۹'}

// LatinDigits rewrites the digit shapes someone may type - Persian
// (U+06F0..) or Arabic (U+0660..) - into the Latin ones every parser
// expects. A page that counts in Persian digits invites them back.
func LatinDigits(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '۰' && r <= '۹':
			return '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			return '0' + (r - '٠')
		}
		return r
	}, s)
}

// shapeDigits rewrites the digits of a string that came back from a
// library, leaving one that still carries a Latin word alone: "399 B"
// half-converted reads worse than either script does whole.
func (g GlobalRenderData) shapeDigits(s string) string {
	if g.Lang != "fa" || strings.ContainsFunc(s, isLatinLetter) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return persianDigits[r-'0']
		}
		return r
	}, s)
}

func isLatinLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// SecondaryDay is the day number of the same date in the secondary
// calendar, empty when none is chosen. The first day of a month carries
// its month name, the way the primary labels do.
func (g *GlobalRenderData) SecondaryDay(t time.Time) string {
	cal, ok := g.secondary()
	if !ok {
		return ""
	}
	_, m, d := cal.Date(g.InTimezone(t))
	if d == 1 {
		return g.printer().Sprintf("%d %s", d, g.shortMonth(cal, m))
	}
	return g.Num(d)
}

// weekAnchor is the Thursday of the seven days beginning at the given
// day. A grid row is labelled by the week that day falls in, which is
// ISO 8601's own rule (the week owning the Thursday) and is therefore
// right however the row is aligned: a row starting on Sunday spans two
// ISO weeks, and this names the one the row mostly is.
func weekAnchor(rowStart time.Time) time.Time {
	iso := (int(rowStart.Weekday())+6)%7 + 1 // Monday 1 ... Sunday 7
	return rowStart.AddDate(0, 0, (4-iso+7)%7)
}

// weekOf counts the week a day falls in, in the given calendar: ISO
// 8601's for the Gregorian one, and for the Solar Hijri one the weeks
// from Nowruz, beginning on Saturday, which is a different number from
// a different origin, so glossing the ISO number would be a translation
// of the wrong thing.
func weekOf(cal CalendarSystem, day time.Time) int {
	if cal.Name() == gregorianName {
		_, week := day.ISOWeek()
		return week
	}
	y, _, _ := cal.Date(day)
	newYear := shDay(y, 1, 1, day.Location())
	offset := (int(newYear.Weekday()) - int(cal.WeekStarts()) + 7) % 7
	return (cal.YearDay(day)-1+offset)/7 + 1
}

// WeekNumber is the week a grid row belongs to, in the primary calendar.
func (g *GlobalRenderData) WeekNumber(rowStart time.Time) string {
	return g.Num(weekOf(g.primary(), weekAnchor(g.InTimezone(rowStart))))
}

// WeekTitle names the week in words, since a bare number in a column of
// its own says nothing about what it counts.
func (g *GlobalRenderData) WeekTitle(rowStart time.Time) string {
	return g.Tf("calendar.week", g.WeekNumber(rowStart))
}

// SecondaryWeek is the same physical week counted in the secondary
// calendar, empty when none is chosen.
func (g *GlobalRenderData) SecondaryWeek(rowStart time.Time) string {
	cal, ok := g.secondary()
	if !ok {
		return ""
	}
	return g.Num(weekOf(cal, weekAnchor(g.InTimezone(rowStart))))
}

// SecondaryMonthYear names the secondary months a primary month spans,
// empty when no secondary calendar is chosen.
func (g *GlobalRenderData) SecondaryMonthYear(t time.Time) string {
	cal, ok := g.secondary()
	if !ok {
		return ""
	}
	start := g.primary().MonthStart(g.InTimezone(t))
	end := g.primary().AddMonths(start, 1).AddDate(0, 0, -1)
	fy, fm, _ := cal.Date(start.Add(12 * time.Hour))
	ly, lm, _ := cal.Date(end.Add(12 * time.Hour))
	first, last := cal.MonthName(fm, g.Lang), cal.MonthName(lm, g.Lang)
	if fm == lm && fy == ly {
		return fmt.Sprintf("%s %s", first, g.year(fy))
	}
	if fy == ly {
		return fmt.Sprintf("%s – %s %s", first, last, g.year(ly))
	}
	return fmt.Sprintf("%s %s – %s %s", first, g.year(fy), last, g.year(ly))
}

// SecondaryDate is the full secondary date of one day.
func (g *GlobalRenderData) SecondaryDate(t time.Time) string {
	cal, ok := g.secondary()
	if !ok || t.IsZero() {
		return ""
	}
	y, m, d := cal.Date(g.InTimezone(t))
	return g.printer().Sprintf("%d %s %s", d, cal.MonthName(m, g.Lang), g.year(y))
}

// CalendarDayLabel keeps ordinary cells to a bare day number and repeats a
// compact month name only on the first day of each month.
func (g *GlobalRenderData) CalendarDayLabel(t time.Time) string {
	cal := g.primary()
	_, m, d := cal.Date(g.InTimezone(t))
	if d != 1 {
		return g.Num(d)
	}
	month := g.shortMonth(cal, m)
	switch g.Lang {
	case "de":
		return g.printer().Sprintf("%d. %s", d, month)
	case "es", "fa":
		return g.printer().Sprintf("%d %s", d, month)
	default:
		return g.printer().Sprintf("%s %d", month, d)
	}
}

// MonthYear renders the calendar heading for t's month.
func (g *GlobalRenderData) MonthYear(t time.Time) string {
	y, _, _ := g.primary().Date(g.InTimezone(t))
	return fmt.Sprintf("%s %s", g.MonthName(t), g.year(y))
}

// LongDate renders a translated day heading without relying on the
// process locale (time.Format only knows English names).
func (g *GlobalRenderData) LongDate(t time.Time) string {
	weekday := g.WeekdayName(t)
	month := g.MonthName(t)
	y, _, d := g.primary().Date(g.InTimezone(t))
	switch g.Lang {
	case "fa":
		return g.printer().Sprintf("%s، %d %s %s", weekday, d, month, g.year(y))
	case "de":
		return g.printer().Sprintf("%s, %d. %s %s", weekday, d, month, g.year(y))
	case "es":
		return g.printer().Sprintf("%s, %d de %s de %s", weekday, d, month, g.year(y))
	default:
		return g.printer().Sprintf("%s, %s %d, %s", weekday, month, d, g.year(y))
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
		Title:          BrandName,
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
		global.Brand = BrandName
		global.ProjectURL = ctx.Server.Options.ProjectURL
		global.Language = ctx.Language()
		global.LanguageChoices = LanguageChoices()
		global.Year = time.Now().Year()
		global.Primary, global.Secondary = ctx.Calendars()
		global.ColorScheme = ctx.ColorScheme()
		global.Theme = ctx.Theme()
		global.TextSize = ctx.TextSize()
	}

	if isactx {
		global.Unified = ctx.Unified
		global.AccountColors = ctx.AccountColors()
		global.AlignByScript = ctx.AlignByScript()
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

// Tf is T with a count and arguments: the plural form the count calls
// for, filled in with numbers written the way that language writes them.
func (ctx *Context) Tf(key string, args ...interface{}) string {
	g := GlobalRenderData{Lang: requestLanguage(ctx)}
	return g.Tf(key, args...)
}

// T translates a namespaced string key into the request's UI
// language, for strings built on the Go side.
func (ctx *Context) T(key string) string {
	return translate(requestLanguage(ctx), key)
}

// PageLanguage is the UI language this request resolves to. A viewer
// rendering part of a message needs it to say whether that part is
// written in another one.
func (ctx *Context) PageLanguage() string {
	return requestLanguage(ctx)
}

// LanguageName is the chosen language in its own name, empty while the
// browser is being followed - the page says so in its own words.
func (g GlobalRenderData) LanguageName() string {
	for _, c := range g.LanguageChoices {
		if c.Code == g.Language {
			return c.Name
		}
	}
	return ""
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

// MonthYearIn and LongDateIn translate a heading for the request's
// language and calendar, for titles built before the render data exists.
func (ctx *Context) MonthYearIn(t time.Time) string {
	return ctx.dates().MonthYear(t)
}

func (ctx *Context) LongDateIn(t time.Time) string {
	return ctx.dates().LongDate(t)
}

func (ctx *Context) dates() *GlobalRenderData {
	g := &GlobalRenderData{Lang: requestLanguage(ctx)}
	g.Primary, g.Secondary = ctx.Calendars()
	return g
}

// PageTitle is the browser title: the page's subject and the brand,
// or the brand alone on pages that name nothing.
func (g *GlobalRenderData) PageTitle() string {
	if g.Title == "" || g.Title == BrandName {
		return BrandName
	}
	return g.Title + " - " + BrandName
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

// Explained is one term and what it means, for the disclosure a card
// offers beside a row of names. A tooltip is a hover, and a phone has
// none.
type Explained struct {
	Term string
	Hint string
}

// RailRow is one place under an account in a section's rail: a page
// the section offers per account, such as an account's signatures.
type RailRow struct {
	Label  string
	Href   string
	Active bool
}
