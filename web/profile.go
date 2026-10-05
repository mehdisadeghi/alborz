package web

import (
	"cmp"
	"fmt"
	"html/template"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/calsys"
	"git.mehdix.org/alborz/internal/locale"
)

// Profile returns the session's profile while signed in, else the browser's cookie copy.
func (ctx *Context) Profile() alborz.Profile {
	if s := ctx.lookupSession(); s != nil && len(ctx.accountLogins()) > 0 {
		if p, ok := s.Profile(); ok {
			return p
		}
	}
	return ctx.cookieProfile()
}

// Reading returns the reading settings, with defaults applied.
func (ctx *Context) Reading() alborz.Reading {
	r := ctx.Profile().Reading
	if r.MessagesPerPage <= 0 {
		r.MessagesPerPage = alborz.DefaultMessagesPerPage
	}
	r.FirstDayOfWeek = cmp.Or(r.FirstDayOfWeek, alborz.DefaultFirstDayOfWeek)
	if r.Secondary == calsys.Of(r.Primary).Name() {
		r.Secondary = ""
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

// UpdateProfile has edit change the profile every account signed in holds, and the session takes the first
// account's; it fails only when every account refuses. The session's copy may be older than what another
// browser saved since, so edit changes the stored profile, in turn with every other change of it.
func (ctx *Context) UpdateProfile(edit func(*alborz.Profile)) error {
	logins := ctx.accountLogins()
	if len(logins) == 0 {
		p := ctx.cookieProfile()
		edit(&p)
		ctx.setProfileCookies(p.Display)
		return nil
	}
	var refused error
	var kept *alborz.Profile
	for _, login := range logins {
		// An account that holds none yet starts from the one in force.
		p := ctx.Profile()
		err := login.Update(ctx.Request().Context(), alborz.ProfileKey, &p, func() error {
			edit(&p)
			return nil
		})
		if err != nil {
			ctx.Logger().Printf("profile: %s did not keep it: %v", login.Username(), err)
			refused = err
			continue
		}
		if kept == nil {
			kept = &p
		}
	}
	if kept == nil {
		return refused
	}
	ctx.putProfile(*kept)
	return nil
}

// TakeProfile applies the account's profile to every account signed in.
func (ctx *Context) TakeProfile(login *alborz.Login) error {
	theirs, err := loadProfile(ctx, login)
	if err != nil {
		return err
	}
	if theirs == nil {
		return ctx.KeepProfile(login)
	}
	return ctx.UpdateProfile(func(p *alborz.Profile) { *p = *theirs })
}

// KeepProfile writes the profile in force over the one the account holds.
func (ctx *Context) KeepProfile(login *alborz.Login) error {
	return login.Store().Put(ctx.Request().Context(), alborz.ProfileKey, ctx.Profile())
}

func (ctx *Context) putProfile(p alborz.Profile) {
	s := ctx.Session()
	s.setProfile(p)
	ctx.setProfileCookies(p.Display)
	if err := ctx.Server.Sessions.Save(s); err != nil {
		ctx.Logger().Printf("failed to write the session: %v", err)
	}
}

// loadProfile returns nil where the account holds none.
func loadProfile(ctx *Context, login *alborz.Login) (*alborz.Profile, error) {
	var p alborz.Profile
	err := login.Store().Get(ctx.Request().Context(), alborz.ProfileKey, &p)
	if err == alborz.ErrNoStoreEntry {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// joinProfile settles the profile when an account signs in; a later account
// holding a different one is asked about.
func (ctx *Context) joinProfile(login *alborz.Login, first bool) {
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
			{Label: ctx.T("profile.take"), Path: ctx.Path("settings.profile.take"), Fields: fields},
			{Label: ctx.T("profile.discard"), Path: ctx.Path("settings.profile.discard"), Fields: fields},
		},
	})
}

type profileChange struct{ label, theirs, ours string }

// profileDiff compares rendered values: two that read the same are the same choice.
func (ctx *Context) profileDiff(theirs, ours alborz.Profile) []profileChange {
	g := GlobalRenderData{Lang: requestLanguage(ctx)}
	var out []profileChange
	add := func(label string, show func(alborz.Profile) string) {
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
	add("settings.perpage", func(p alborz.Profile) string {
		return g.Num(cmp.Or(max(p.Reading.MessagesPerPage, 0), alborz.DefaultMessagesPerPage))
	})
	add("settings.timezone", func(p alborz.Profile) string { return or(p.Reading.Timezone, "settings.tzauto") })
	add("settings.firstday", func(p alborz.Profile) string {
		return g.WeekdayName(p.Reading.WeekStart())
	})
	add("settings.preferhtml", func(p alborz.Profile) string { return onOff(p.Reading.PreferHTML) })
	add("settings.calendar", func(p alborz.Profile) string { return calendar(p.Reading.Primary, "settings.gregorian") })
	add("settings.secondary", func(p alborz.Profile) string { return calendar(p.Reading.Secondary, "settings.secondarynone") })
	add("settings.replyposition", func(p alborz.Profile) string {
		if p.Writing.ReplyBelowQuote {
			return g.T("settings.replybottom")
		}
		return g.T("settings.replytop")
	})
	add("settings.directionpart", func(p alborz.Profile) string { return onOff(!p.Writing.NoDirectionPart) })
	add("settings.theme", func(p alborz.Profile) string {
		if p.Display.Theme == "" {
			return g.T("settings.themedefault")
		}
		return g.T("theme." + p.Display.Theme)
	})
	add("settings.textsize", func(p alborz.Profile) string {
		return g.T("settings.text" + cmp.Or(p.Display.TextSize, "default"))
	})
	add("settings.alignscript", func(p alborz.Profile) string { return onOff(p.Display.AlignByScript) })
	add("settings.status", func(p alborz.Profile) string {
		return selectedLabels(g, ctx.Server.Status, p.Display.Status, func(seg Segment) string { return seg.Name }, ".status")
	})
	add("settings.addons", func(p alborz.Profile) string {
		return selectedLabels(g, ctx.Server.Addons, p.Display.Addons, func(a Addon) string { return a.Name }, ".addon")
	})
	add("settings.place", func(p alborz.Profile) string { return or(p.Place.Name, "settings.placenone") })
	add("settings.language", func(p alborz.Profile) string {
		return or(locale.Name(p.Display.Language), "settings.langauto")
	})
	return out
}

func selectedLabels[T any](g GlobalRenderData, offered []T, selected []string, name func(T) string, suffix string) string {
	var on []string
	for _, item := range offered {
		if name := name(item); slices.Contains(selected, name) {
			on = append(on, g.T(name+suffix))
		}
	}
	if len(on) == 0 {
		return g.T("profile.off")
	}
	return strings.Join(on, " · ")
}

// SettingsRail is the settings pages' rail: what this browser keeps, then each account's pages.
func SettingsRail(ctx *Context) map[string][]RailRow {
	path := ctx.Request().URL.Path
	// Under no account: what this browser keeps.
	rows := map[string][]RailRow{"": {
		{Label: ctx.T("settings.general"), Href: ctx.Path("settings.profile"), Active: path == ctx.Path("settings.profile")},
		{Label: ctx.T("passkeys.title"), Href: ctx.Path("settings.passkeys"), Active: strings.HasPrefix(path, ctx.Path("settings.passkeys"))},
	}}
	for _, s := range ctx.Server.Status {
		if s.Page != "" {
			rows[""] = append(rows[""], RailRow{Label: ctx.T(s.Name + ".status"), Href: ctx.Path(s.Page), Active: path == ctx.Path(s.Page)})
		}
	}
	for _, account := range ctx.Accounts() {
		scoped := ctx.Login != nil && account.Username == ctx.Login.Username()
		q := AccountQuery(account.Username)
		rows[account.Username] = []RailRow{
			{Label: ctx.T("settings.account"), Href: ctx.Path("mail.account") + q, Active: scoped && path == ctx.Path("mail.account")},
			{Label: ctx.T("settings.signatures"), Href: ctx.Path("mail.signatures") + q, Active: scoped && strings.HasPrefix(path, ctx.Path("mail.signatures"))},
			{Label: ctx.T("settings.servers"), Href: ctx.Path("settings.servers") + q, Active: scoped && strings.HasPrefix(path, ctx.Path("settings.servers"))},
			{Label: ctx.T("sessions.title"), Href: ctx.Path("settings.sessions") + q, Active: scoped && path == ctx.Path("settings.sessions")},
			{Label: ctx.T("notifications.title"), Href: ctx.Path("settings.notifications") + q, Active: scoped && strings.HasPrefix(path, ctx.Path("settings.notifications"))},
			{Label: ctx.T("settings.password"), Href: ctx.Path("settings.password") + q, Active: scoped && path == ctx.Path("settings.password")},
			{Label: ctx.T("settings.data"), Href: ctx.Path("settings.data") + q, Active: scoped && path == ctx.Path("settings.data")},
			{Label: ctx.T("settings.encryption"), Href: ctx.Path("settings.encryption") + q, Active: scoped && path == ctx.Path("settings.encryption")},
		}
	}
	return rows
}

// KeepChanges sets on stored each field mine changed from base, and a list by the names added and taken
// away, so a field the save did not change stays as another save left it.
func KeepChanges(stored, base, mine reflect.Value) {
	for i := range base.NumField() {
		s, b, m := stored.Field(i), base.Field(i), mine.Field(i)
		switch {
		case b.Kind() == reflect.Struct:
			KeepChanges(s, b, m)
		case reflect.DeepEqual(b.Interface(), m.Interface()):
		case b.Kind() == reflect.Slice:
			was, now := b.Interface().([]string), m.Interface().([]string)
			kept := slices.DeleteFunc(slices.Clone(s.Interface().([]string)), func(name string) bool {
				return slices.Contains(was, name) && !slices.Contains(now, name)
			})
			for _, name := range now {
				if !slices.Contains(was, name) && !slices.Contains(kept, name) {
					kept = append(kept, name)
				}
			}
			s.Set(reflect.ValueOf(kept))
		default:
			s.Set(m)
		}
	}
}
