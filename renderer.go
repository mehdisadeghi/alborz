package alborz

import (
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template/parse"
	"time"

	"github.com/labstack/echo/v4"

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
	LanguageChoices []LanguageChoice
	Year            int
	// Primary is the calendar the page counts in, Secondary the one glossed beside it, empty for none.
	Primary   string
	Secondary string

	Timezone *time.Location

	// 0=Sunday, 1=Monday (default), 6=Saturday
	FirstDayOfWeek int

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

	// SchemePinned hides the scheme toggle for a theme written for one scheme only.
	SchemePinned bool

	CustomCSS bool

	// TextSize is empty for the default size.
	TextSize string

	Lang string
}

// Dir is the writing direction of the UI language.
func (g *GlobalRenderData) Dir() string {
	if g.Lang == "fa" {
		return "rtl"
	}
	return "ltr"
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
	return translate(brd.GlobalData.Lang, key)
}

// Since dates a message in the reader's language, as short as a list column.
func (g *GlobalRenderData) Since(t time.Time) string {
	unit, n := elapsed(g.InTimezone(t), g.InTimezone(time.Now()))
	if unit == "" {
		return g.T("since.now")
	}
	key := "since.ago"
	if n < 0 {
		key, n = "since.ahead", -n
	}
	// A year count is never grouped: "2025 yrs ago", not "2,025".
	span := g.printer().Sprintf(translateCount(g.Lang, "since."+unit, n), number.Decimal(n, number.NoSeparator()))
	return g.Tf(key, span)
}

var secondsIn = []struct {
	unit string
	n    int64
}{
	{"week", int64(7 * 24 * time.Hour / time.Second)},
	{"day", int64(24 * time.Hour / time.Second)},
	{"hour", int64(time.Hour / time.Second)},
	{"minute", int64(time.Minute / time.Second)},
	{"second", 1},
}

func elapsed(t, now time.Time) (string, int) {
	sign, s, e := 1, t, now
	if e.Before(s) {
		sign, s, e = -1, e, s
	}
	years := e.Year() - s.Year()
	if s.AddDate(years, 0, 0).After(e) {
		years--
	}
	if years > 0 {
		return "year", sign * years
	}
	months := (e.Year()-s.Year())*12 + int(e.Month()-s.Month())
	if s.AddDate(0, months, 0).After(e) {
		months--
	}
	if months > 0 {
		return "month", sign * months
	}
	secs := e.Unix() - s.Unix()
	for _, u := range secondsIn {
		if secs >= u.n {
			return u.unit, sign * int(secs/u.n)
		}
	}
	return "", 0
}

// FormatDate states a full date and time.
func (g *GlobalRenderData) FormatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return fmt.Sprintf("%s%s %s", g.LongDate(t), g.comma(), g.FormatTime(t))
}

func (g *GlobalRenderData) comma() string {
	if g.Lang == "fa" {
		return "،"
	}
	return ","
}

// ShortDate is a bare date in the secondary calendar when one is chosen, without the current year.
func (g *GlobalRenderData) ShortDate(t time.Time) string {
	cal := g.PrimaryCalendar()
	year, m, day := cal.Date(g.InTimezone(t))
	thisYear, _, _ := cal.Date(g.InTimezone(time.Now()))
	month := g.ShortMonth(cal, m)
	p := g.printer()
	var s, yearSep string
	switch g.Lang {
	case "de":
		s, yearSep = p.Sprintf("%d. %s", day, month), " "
	case "es", "fa":
		s, yearSep = p.Sprintf("%d %s", day, month), " "
	default:
		s, yearSep = p.Sprintf("%s %d", month, day), ", "
	}
	if year != thisYear {
		s = s + yearSep + g.FormatYear(year)
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

// weekOne is a Sunday, so adding a weekday index names that weekday.
var weekOne = time.Date(2023, time.January, 1, 12, 0, 0, 0, time.UTC)

// WeekdayName translates the weekday of t.
func (g *GlobalRenderData) WeekdayName(t time.Time) string {
	return g.T(fmt.Sprintf("calendar.weekday.%d", g.InTimezone(t).Weekday()))
}

// MonthName translates the month of t, in the primary calendar.
func (g *GlobalRenderData) MonthName(t time.Time) string {
	cal := g.PrimaryCalendar()
	_, m, _ := cal.Date(g.InTimezone(t))
	return cal.MonthName(m, g.Lang)
}

// ShortMonth names a month of cal as a narrow column does.
func (g *GlobalRenderData) ShortMonth(cal CalendarSystem, month int) string {
	if cal.Name() != GregorianName {
		return cal.MonthName(month, g.Lang)
	}
	return g.T(fmt.Sprintf("calendar.gmonthshort.%d", month))
}

// PrimaryCalendar is the calendar the page counts in.
func (g *GlobalRenderData) PrimaryCalendar() CalendarSystem { return Calendar(g.Primary) }

// SecondaryCalendar is the calendar glossed beside the primary one; false for none.
func (g *GlobalRenderData) SecondaryCalendar() (CalendarSystem, bool) {
	if g.Secondary == "" || g.Secondary == g.PrimaryCalendar().Name() {
		return nil, false
	}
	return Calendar(g.Secondary), true
}

var replyPrefix = regexp.MustCompile(`^\s*(?i:(re|aw|sv|antw|fw|fwd|rv|tr)\s*(\[\d+\])?\s*:|\[[^\]]{1,40}\])\s*`)

// SubjectDir is the direction a subject reads in, ignoring reply prefixes that would lead with Latin letters.
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
	stripped = strings.TrimSpace(stripped)
	if stripped == "" {
		return "auto"
	}
	// x/text's Direction panics on a paragraph that has not been ordered.
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

// ContentLang names the language of interface text, empty where it matches the page.
func (g *GlobalRenderData) ContentLang(s string) string {
	return ContentLang(s, g.Lang)
}

// MessageLang is ContentLang for what a message itself says.
func (g *GlobalRenderData) MessageLang(s string) string {
	return MessageLang(s, g.Lang)
}

// ShortAccount names an account by its domain unless two accounts share it.
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

// AccountTrack is the width of the widest account label, so a column of them aligns.
func (g GlobalRenderData) AccountTrack() template.CSS {
	n := 0
	for _, a := range g.Accounts {
		if l := len([]rune(g.AccountLabel(a.Username))); l > n {
			n = l
		}
	}
	return template.CSS(fmt.Sprintf("%dch", n))
}

var printers = map[string]*message.Printer{}

func init() {
	for _, code := range languages {
		printers[code] = message.NewPrinter(language.MustParse(code))
	}
}

// Value receivers: templates box the global in a tuple, leaving pointer methods out of reach.
func (g GlobalRenderData) printer() *message.Printer {
	if p, ok := printers[g.Lang]; ok {
		return p
	}
	return printers["en"]
}

// Num writes a grouped count as the page's language writes one.
func (g GlobalRenderData) Num(n int) string {
	return g.printer().Sprintf("%d", n)
}

// FormatYear writes a year in the page's digits, never grouped.
func (g GlobalRenderData) FormatYear(n int) string {
	return g.printer().Sprint(number.Decimal(n, number.NoSeparator()))
}

// T translates a key in the page's language.
func (g GlobalRenderData) T(key string) string {
	return translate(g.Lang, key)
}

// Tf translates a format string and fills it in the page's language.
func (g GlobalRenderData) Tf(key string, args ...interface{}) string {
	count := 1
	if len(args) > 0 {
		if n, ok := args[0].(int); ok {
			count = n
		}
	}
	return g.printer().Sprintf(translateCount(g.Lang, key, count), args...)
}

var persianDigits = [10]rune{'۰', '۱', '۲', '۳', '۴', '۵', '۶', '۷', '۸', '۹'}

// LatinDigits rewrites Persian and Arabic digits into Latin ones for parsing.
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

// ReadInt reads a typed whole number in either digit shape, with the language's thousands separator.
func ReadInt(s string) (int, error) {
	return strconv.Atoi(strings.NewReplacer("٬", "", ",", "").Replace(LatinDigits(strings.TrimSpace(s))))
}

// A string still carrying a Latin word is left alone: half-converted reads worse than either script.
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

// SecondaryDate is the full secondary date of one day.
func (g *GlobalRenderData) SecondaryDate(t time.Time) string {
	cal, ok := g.SecondaryCalendar()
	if !ok || t.IsZero() {
		return ""
	}
	y, m, d := cal.Date(g.InTimezone(t))
	return g.printer().Sprintf("%d %s %s", d, cal.MonthName(m, g.Lang), g.FormatYear(y))
}

// MonthYear renders the calendar heading for t's month.
func (g *GlobalRenderData) MonthYear(t time.Time) string {
	y, _, _ := g.PrimaryCalendar().Date(g.InTimezone(t))
	return fmt.Sprintf("%s %s", g.MonthName(t), g.FormatYear(y))
}

// LongDate renders a translated day heading; time.Format knows only English names.
func (g *GlobalRenderData) LongDate(t time.Time) string {
	weekday := g.WeekdayName(t)
	month := g.MonthName(t)
	y, _, d := g.PrimaryCalendar().Date(g.InTimezone(t))
	switch g.Lang {
	case "fa":
		return g.printer().Sprintf("%s، %d %s %s", weekday, d, month, g.FormatYear(y))
	case "de":
		return g.printer().Sprintf("%s, %d. %s %s", weekday, d, month, g.FormatYear(y))
	case "es":
		return g.printer().Sprintf("%s, %d de %s de %s", weekday, d, month, g.FormatYear(y))
	default:
		return g.printer().Sprintf("%s, %s %d, %s", weekday, month, d, g.FormatYear(y))
	}
}

// RenderData is implemented by template data structs.
type RenderData interface {
	// GlobalData returns a pointer to the global render data.
	Global() *GlobalRenderData
}

// NewBaseRenderData initializes a new BaseRenderData.
func NewBaseRenderData(ectx echo.Context) *BaseRenderData {
	ctx, isactx := ectx.(*Context)

	lang := requestLanguage(ectx)
	global := GlobalRenderData{
		Path:           strings.Split(ectx.Request().URL.Path, "/")[1:],
		Title:          BrandName,
		URL:            ectx.Request().URL,
		FirstDayOfWeek: 1,
		Lang:           lang,

		Asset: func(name string) string {
			if !isactx {
				return "/assets/" + name
			}
			return ctx.Server.AssetURL(name)
		},
	}

	if isactx {
		global.Version, global.Revision = Build()
		_, global.Shell = ctx.Server.shell()
		if s := ctx.lookupSession(); s != nil {
			if lock := s.Lock(); len(lock.Passkeys) > 0 {
				global.LockSeconds = int(lock.Wait() / time.Second)
			}
		}
		// A person reads the brand in their own script; what a machine reads stays BrandName.
		global.Brand = translate(lang, "brand.name")
		global.ProjectURL = ctx.Server.Options.ProjectURL
		global.Language = ctx.Language()
		global.LanguageChoices = LanguageChoices()
		global.Year = time.Now().Year()
		global.Primary, global.Secondary = ctx.Calendars()
		global.Theme = ctx.Theme()
		global.TextSize = ctx.TextSize()
	}

	if isactx {
		global.Unified = ctx.Unified
		global.AlignByScript = ctx.AlignByScript()
		global.CustomCSS = ctx.Server.custom
		global.SchemePinned = ctx.ThemeScheme() != ""
		global.URLAccount = ctx.urlAccount
		global.From = ctx.From()
		global.Scope, global.Merged = ctx.urlAccount, ctx.Unified
		// Values.Encode would escape the raw values again.
		req := ctx.Request().URL
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
		global.Notice = ctx.PopNotice()
	}

	if isactx && ctx.Login != nil {
		global.LoggedIn = true
		global.Username = ctx.Login.username
		global.Accounts = ctx.Accounts()
		// Unset means times keep their stored zones.
		if loc := UserLocation(ctx); loc != time.UTC {
			global.Timezone = loc
		}
		global.FirstDayOfWeek = ctx.Reading().FirstDayOfWeek
	}

	return &BaseRenderData{GlobalData: global}
}

// Tf is T with a count and arguments, in the plural form the count calls for.
func (ctx *Context) Tf(key string, args ...interface{}) string {
	g := GlobalRenderData{Lang: requestLanguage(ctx)}
	return g.Tf(key, args...)
}

// T translates a namespaced string key into the request's UI language.
func (ctx *Context) T(key string) string {
	return translate(requestLanguage(ctx), key)
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
func queryLanguage(ectx echo.Context) string {
	if v := ectx.QueryParam("lang"); IsLanguage(v) {
		return v
	}
	return ""
}

func requestLanguage(ectx echo.Context) string {
	if v := queryLanguage(ectx); v != "" {
		return v
	}
	if ctx, ok := ectx.(*Context); ok {
		if v := ctx.Language(); v != "" {
			return v
		}
	} else if c, err := ectx.Cookie(langCookieName); err == nil && IsLanguage(c.Value) {
		return c.Value
	}
	return MatchLanguage(ectx.Request().Header.Get("Accept-Language"))
}

// RenderInfo renders a full page carrying one explanatory sentence.
func RenderInfo(ctx *Context, code int, message string) error {
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

// MonthYearIn and LongDateIn translate a heading before the render data exists.
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

// PageTitle is the browser title: the page's subject and the brand, or the brand alone.
func (g *GlobalRenderData) PageTitle() string {
	brand := g.Brand
	if brand == "" {
		brand = BrandName
	}
	if g.Title == "" || g.Title == BrandName {
		return brand
	}
	return g.Title + " - " + brand
}

// renderer parses each page into its own clone of the layout and partials, so pages' blocks never collide.
type renderer struct {
	partials *template.Template
	pages    map[string]*template.Template
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

	// A notice raised after the base data was built belongs to this page, not the next.
	if g := renderData.Global(); g.Notice == nil {
		g.Notice = ctx.PopNotice()
	}

	start := time.Now()
	var err error
	if page, ok := r.pages[name]; ok {
		err = page.ExecuteTemplate(w, "layout", data)
	} else {
		err = r.partials.ExecuteTemplate(w, name, data)
	}
	ctx.timing.add("render", start, time.Now())
	return err
}

// loadTemplates reads layout.html and partials/ into one set, and each file under pages/ into a clone of it,
// named by its path without pages/ and .html.
func loadTemplates(files fs.FS, funcs template.FuncMap) (*renderer, error) {
	partials := template.New("").Funcs(funcs)
	owner := map[string]string{}
	shared := []string{"layout.html"}
	err := fs.WalkDir(files, "partials", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			shared = append(shared, name)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, name := range shared {
		src, defined, err := parseFile(files, name, funcs)
		if err != nil {
			return nil, err
		}
		// html/template lets a second definition replace the first without a word.
		for _, t := range defined {
			if first, ok := owner[t]; ok {
				return nil, fmt.Errorf("%s: %q is defined in %s already", name, t, first)
			}
			owner[t] = name
		}
		if _, err := partials.New(name).Parse(src); err != nil {
			return nil, err
		}
	}

	pages := map[string]*template.Template{}
	err = fs.WalkDir(files, "pages", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		src, defined, err := parseFile(files, name, funcs)
		if err != nil {
			return err
		}
		// A page fills the layout's blocks, or a partial's empty one; replacing a partial would change it for this page alone.
		for _, t := range defined {
			if s := partials.Lookup(t); s != nil && owner[t] != "layout.html" && !parse.IsEmptyTree(s.Tree.Root) {
				return fmt.Errorf("%s: %q is a partial in %s, not a block", name, t, owner[t])
			}
		}
		page, err := partials.Clone()
		if err != nil {
			return err
		}
		if _, err := page.New(name).Parse(src); err != nil {
			return err
		}
		pages[strings.TrimSuffix(strings.TrimPrefix(name, "pages/"), ".html")] = page
		return resolved(page)
	})
	if err != nil {
		return nil, err
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
