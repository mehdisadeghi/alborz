package alborz

import (
	"cmp"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"
)

// Profile is the person's own choices, kept on every account signed in (ADR 52).
type Profile struct {
	Reading Reading
	Writing Writing
	Display Display
}

type Reading struct {
	MessagesPerPage int
	Timezone        string
	FirstDayOfWeek  int // 0 = Sunday
	PreferHTML      bool
	// Primary and Secondary are calendar systems; empty means Gregorian and none.
	Primary   string
	Secondary string
}

type Writing struct {
	ReplyBelowQuote bool
	// NoDirectionPart is negative so the zero value keeps the part.
	NoDirectionPart bool
}

// Display is empty where it follows the default.
type Display struct {
	Theme         string
	TextSize      string
	AlignByScript bool
	Language      string
}

const profileKey = "profile"

// DefaultMessagesPerPage is the page size of a reader who never chose one.
const DefaultMessagesPerPage = 50

// Profile returns the session's profile while signed in, else the browser's cookie copy.
func (ctx *Context) Profile() Profile {
	if s := ctx.lookupSession(); s != nil && len(ctx.accountLogins()) > 0 {
		if p, ok := s.Profile(); ok {
			return p
		}
	}
	return ctx.cookieProfile()
}

// Reading returns the reading settings, with defaults applied.
func (ctx *Context) Reading() Reading {
	r := ctx.Profile().Reading
	if r.MessagesPerPage <= 0 {
		r.MessagesPerPage = DefaultMessagesPerPage
	}
	return r
}

// UserLocation resolves the reader's timezone.
func UserLocation(ctx *Context) *time.Location {
	tz := ctx.Reading().Timezone
	if tz == "" {
		if c, err := ctx.Cookie(timezoneCookieName); err == nil {
			tz = c.Value
		}
	}
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.UTC
}

// SetProfile applies p and writes it to every account signed in; it fails only
// when every account refuses.
func (ctx *Context) SetProfile(p Profile) error {
	logins := ctx.accountLogins()
	if len(logins) == 0 {
		ctx.setProfileCookies(p.Display)
		return nil
	}
	var refused error
	kept := 0
	for _, login := range logins {
		if err := login.Store().Put(ctx.Request().Context(), profileKey, p); err != nil {
			ctx.Logger().Printf("profile: %s did not keep it: %v", login.Username(), err)
			refused = err
			continue
		}
		kept++
	}
	if kept == 0 {
		return refused
	}
	ctx.putProfile(p)
	return nil
}

// TakeProfile applies the account's profile to every account signed in.
func (ctx *Context) TakeProfile(login *Login) error {
	theirs, err := loadProfile(ctx, login)
	if err != nil {
		return err
	}
	if theirs == nil {
		return ctx.KeepProfile(login)
	}
	return ctx.SetProfile(*theirs)
}

// KeepProfile writes the profile in force over the one the account holds.
func (ctx *Context) KeepProfile(login *Login) error {
	return login.Store().Put(ctx.Request().Context(), profileKey, ctx.Profile())
}

func (ctx *Context) putProfile(p Profile) {
	s := ctx.Session()
	s.setProfile(p)
	ctx.setProfileCookies(p.Display)
	if err := ctx.Server.Sessions.Save(s); err != nil {
		ctx.Logger().Printf("failed to write the session: %v", err)
	}
}

// loadProfile returns nil where the account holds none.
func loadProfile(ctx *Context, login *Login) (*Profile, error) {
	var p Profile
	err := login.Store().Get(ctx.Request().Context(), profileKey, &p)
	if err == ErrNoStoreEntry {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// joinProfile settles the profile when an account signs in; a later account
// holding a different one is asked about.
func (ctx *Context) joinProfile(login *Login, first bool) {
	theirs, err := loadProfile(ctx, login)
	if err != nil {
		ctx.Logger().Printf("profile: reading %s's: %v", login.Username(), err)
		return
	}
	if first {
		if theirs != nil {
			ctx.putProfile(*theirs)
			return
		}
		ctx.putProfile(ctx.cookieProfile())
	} else if theirs != nil {
		if diff := ctx.profileDiff(*theirs, ctx.Profile()); len(diff) > 0 {
			ctx.askProfile(login.Username(), diff)
			return
		}
	}
	if err := ctx.KeepProfile(login); err != nil {
		ctx.Logger().Printf("profile: %s did not keep it: %v", login.Username(), err)
	}
}

func (ctx *Context) askProfile(account string, diff []profileChange) {
	var plain, marked []string
	say := ctx.T("profile.field")
	for _, d := range diff {
		plain = append(plain, fmt.Sprintf(say, d.label, d.theirs, d.ours))
		marked = append(marked, fmt.Sprintf(say, template.HTMLEscapeString(d.label),
			"<bdi>"+template.HTMLEscapeString(d.theirs)+"</bdi>",
			"<bdi>"+template.HTMLEscapeString(d.ours)+"</bdi>"))
	}
	fields := url.Values{"account": {account}}
	ctx.Notify(Notice{
		Kind: NoticeWarning,
		Text: fmt.Sprintf(ctx.T("profile.differs"), account, strings.Join(plain, " · ")),
		Markup: template.HTML(fmt.Sprintf(ctx.T("profile.differs"),
			`<bdi class="ltr">`+template.HTMLEscapeString(account)+`</bdi>`, strings.Join(marked, " · "))),
		Actions: []*NoticeAction{
			{Label: ctx.T("profile.take"), Path: "/settings/profile/take", Fields: fields},
			{Label: ctx.T("profile.discard"), Path: "/settings/profile/discard", Fields: fields},
		},
	})
}

type profileChange struct{ label, theirs, ours string }

// profileDiff compares rendered values: two that read the same are the same choice.
func (ctx *Context) profileDiff(theirs, ours Profile) []profileChange {
	g := GlobalRenderData{Lang: requestLanguage(ctx)}
	var out []profileChange
	add := func(label string, show func(Profile) string) {
		if a, b := show(theirs), show(ours); a != b {
			out = append(out, profileChange{g.T(label), a, b})
		}
	}
	or := func(value, none string) string {
		if value == "" {
			return g.T(none)
		}
		return value
	}
	onOff := func(on bool) string {
		if on {
			return g.T("profile.on")
		}
		return g.T("profile.off")
	}
	calendar := func(name, none string) string {
		switch name {
		case "":
			return g.T(none)
		case "shcal":
			return g.T("settings.shcal")
		}
		return g.T("settings.gregorian")
	}
	add("settings.perpage", func(p Profile) string {
		return g.Num(cmp.Or(max(p.Reading.MessagesPerPage, 0), DefaultMessagesPerPage))
	})
	add("settings.timezone", func(p Profile) string { return or(p.Reading.Timezone, "settings.tzauto") })
	add("settings.firstday", func(p Profile) string {
		return g.WeekdayName(weekOne.AddDate(0, 0, p.Reading.FirstDayOfWeek))
	})
	add("settings.preferhtml", func(p Profile) string { return onOff(p.Reading.PreferHTML) })
	add("settings.calendar", func(p Profile) string { return calendar(p.Reading.Primary, "settings.gregorian") })
	add("settings.secondary", func(p Profile) string { return calendar(p.Reading.Secondary, "settings.secondarynone") })
	add("settings.replyposition", func(p Profile) string {
		if p.Writing.ReplyBelowQuote {
			return g.T("settings.replybottom")
		}
		return g.T("settings.replytop")
	})
	add("settings.directionpart", func(p Profile) string { return onOff(!p.Writing.NoDirectionPart) })
	add("settings.theme", func(p Profile) string {
		if p.Display.Theme == "" {
			return g.T("settings.themedefault")
		}
		return ThemeName(p.Display.Theme)
	})
	add("settings.textsize", func(p Profile) string {
		return g.T("settings.text" + cmp.Or(p.Display.TextSize, "default"))
	})
	add("settings.alignscript", func(p Profile) string { return onOff(p.Display.AlignByScript) })
	add("settings.language", func(p Profile) string {
		return or(languageNames[p.Display.Language], "settings.langauto")
	})
	return out
}

// ThemeName returns a theme's file name as a reader reads it.
func ThemeName(name string) string {
	name = strings.ReplaceAll(name, "-", " ")
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}
