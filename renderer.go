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
	"github.com/dromara/carbon/v2/calendar/persian"
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
	// Secondary calendar system shown beside the Gregorian dates,
	// empty for none
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
	c := g.at(t)
	day, year, month := c.Day(), c.Year(), c.ToShortMonthString()
	thisYear := g.at(time.Now()).Year()
	if g.Secondary == shcalName {
		d, now := g.persian(t), g.persian(time.Now())
		day, year, thisYear = d.Day(), d.Year(), now.Year()
		month = g.shMonthName(d)
	}
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
	return fmt.Sprintf("%s %s", g.at(t).ToShortMonthString(), g.year(t.Year()))
}

// MonthName translates the month of t.
func (g *GlobalRenderData) MonthName(t time.Time) string {
	return g.at(t).ToMonthString()
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

// shcalName is the one secondary calendar offered, the Solar Hijri
// (Jalali) one that Iran and Afghanistan count in.
const shcalName = "shcal"

// persian reads t in the secondary calendar.
func (g *GlobalRenderData) persian(t time.Time) *persian.Persian {
	return persian.FromStdTime(g.InTimezone(t))
}

// SecondaryDay is the day number of the same date in the secondary
// calendar, empty when none is chosen. The first day of a month carries
// its month name, the way the Gregorian labels do.
func (g *GlobalRenderData) SecondaryDay(t time.Time) string {
	if g.Secondary != shcalName {
		return ""
	}
	d := g.persian(t)
	if d.Day() == 1 {
		return g.printer().Sprintf("%d %s", d.Day(), g.shMonthName(d))
	}
	return g.Num(d.Day())
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

// WeekNumber is the ISO 8601 week a grid row belongs to.
func (g *GlobalRenderData) WeekNumber(rowStart time.Time) string {
	_, week := weekAnchor(g.InTimezone(rowStart)).ISOWeek()
	return g.Num(week)
}

// WeekTitle names the week in words, since a bare number in a column of
// its own says nothing about what it counts.
func (g *GlobalRenderData) WeekTitle(rowStart time.Time) string {
	return g.Tf("calendar.week", g.WeekNumber(rowStart))
}

// SecondaryWeek is the same physical week counted in the secondary
// calendar, empty when none is chosen. It is a different number from a
// different origin: the Solar Hijri year begins at Nowruz and its weeks
// begin on Saturday, so glossing the ISO number would be a translation
// of the wrong thing.
func (g *GlobalRenderData) SecondaryWeek(rowStart time.Time) string {
	if g.Secondary != shcalName {
		return ""
	}
	day := weekAnchor(g.InTimezone(rowStart))
	d := persian.FromStdTime(day)
	nowruz := persian.NewPersian(d.Year(), 1, 1).ToGregorian().Time
	// Saturday starts the week, so it is the zero of the offset.
	offset := (int(nowruz.Weekday()) + 1) % 7
	return g.Num((shYearDay(d)-1+offset)/7 + 1)
}

// shYearDay is the ordinal day within the Solar Hijri year: the first
// six months hold 31 days and the next five hold 30, which is fixed and
// needs no table.
func shYearDay(d *persian.Persian) int {
	if d.Month() <= 6 {
		return (d.Month()-1)*31 + d.Day()
	}
	return 186 + (d.Month()-7)*30 + d.Day()
}

// SecondaryMonthYear names the secondary months a Gregorian month spans,
// empty when no secondary calendar is chosen.
func (g *GlobalRenderData) SecondaryMonthYear(t time.Time) string {
	if g.Secondary != shcalName {
		return ""
	}
	t = g.InTimezone(t)
	first := g.persian(time.Date(t.Year(), t.Month(), 1, 12, 0, 0, 0, t.Location()))
	last := g.persian(time.Date(t.Year(), t.Month()+1, 0, 12, 0, 0, 0, t.Location()))
	if first.Month() == last.Month() && first.Year() == last.Year() {
		return fmt.Sprintf("%s %s", g.shMonthName(first), g.year(first.Year()))
	}
	if first.Year() == last.Year() {
		return fmt.Sprintf("%s – %s %s", g.shMonthName(first), g.shMonthName(last), g.year(last.Year()))
	}
	return fmt.Sprintf("%s %s – %s %s", g.shMonthName(first), g.year(first.Year()),
		g.shMonthName(last), g.year(last.Year()))
}

// SecondaryDate is the full secondary date of one day.
func (g *GlobalRenderData) SecondaryDate(t time.Time) string {
	if g.Secondary != shcalName || t.IsZero() {
		return ""
	}
	d := g.persian(t)
	return g.printer().Sprintf("%d %s %s", d.Day(), g.shMonthName(d), g.year(d.Year()))
}

// shMonthName spells a Solar Hijri month in Persian for a Persian page
// and transliterates it everywhere else, which is all carbon offers and
// all the other three languages have a convention for.
func (g *GlobalRenderData) shMonthName(d *persian.Persian) string {
	if g.Lang == "fa" {
		return d.ToMonthString(persian.FaLocale)
	}
	return d.ToMonthString(persian.EnLocale)
}

// CalendarDayLabel keeps ordinary cells to a bare day number and repeats a
// compact month name only on the first day of each month.
func (g *GlobalRenderData) CalendarDayLabel(t time.Time) string {
	if t.Day() != 1 {
		return g.Num(t.Day())
	}
	month := g.at(t).ToShortMonthString()
	switch g.Lang {
	case "de":
		return g.printer().Sprintf("%d. %s.", t.Day(), month)
	case "es", "fa":
		return g.printer().Sprintf("%d %s", t.Day(), month)
	default:
		return g.printer().Sprintf("%s %d", month, t.Day())
	}
}

// MonthYear renders the calendar heading for t's month.
func (g *GlobalRenderData) MonthYear(t time.Time) string {
	return fmt.Sprintf("%s %s", g.MonthName(t), g.year(t.Year()))
}

// LongDate renders a translated day heading without relying on the
// process locale (time.Format only knows English names).
func (g *GlobalRenderData) LongDate(t time.Time) string {
	weekday := g.WeekdayName(t)
	month := g.MonthName(t)
	switch g.Lang {
	case "fa":
		return g.printer().Sprintf("%s، %d %s %s", weekday, t.Day(), month, g.year(t.Year()))
	case "de":
		return g.printer().Sprintf("%s, %d. %s %s", weekday, t.Day(), month, g.year(t.Year()))
	case "es":
		return g.printer().Sprintf("%s, %d de %s de %s", weekday, t.Day(), month, g.year(t.Year()))
	default:
		return g.printer().Sprintf("%s, %s %d, %s", weekday, month, t.Day(), g.year(t.Year()))
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
		global.Secondary = ctx.SecondaryCalendar()
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

// MonthYearIn translates a month heading for the request's language,
// for titles built before the render data exists.
func (ctx *Context) MonthYearIn(t time.Time) string {
	g := GlobalRenderData{Lang: requestLanguage(ctx)}
	return g.MonthYear(t)
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
