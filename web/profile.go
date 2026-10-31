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
		ctx.Session().profileAnswered(login.Username())
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
	if err := login.Store().Put(ctx.Request().Context(), alborz.ProfileKey, ctx.Profile()); err != nil {
		return err
	}
	ctx.Session().profileAnswered(login.Username())
	return nil
}

func (s *Session) profileAnswered(account string) {
	s.mu.Lock()
	delete(s.pendingProfiles, account)
	s.mu.Unlock()
}

func (ctx *Context) putProfile(p alborz.Profile) {
	s := ctx.Session()
	s.setProfile(p)
	ctx.setProfileCookies(p.Display)
	if err := ctx.Server.Sessions.Save(s); err != nil {
		ctx.Logger().Printf("failed to write the session: %v", err)
	}
}

// refreshProfile brings a browser's session up to date before a page renders.
func (ctx *Context) refreshProfile(login *alborz.Login) {
	s := ctx.Session()
	s.mu.Lock()
	pending := len(s.pendingProfiles) > 0
	s.mu.Unlock()
	if pending {
		return
	}
	theirs, err := loadProfile(ctx, login)
	if err != nil {
		ctx.Logger().Printf("profile: reading %s's: %v", login.Username(), err)
		return
	}
	if theirs != nil && !reflect.DeepEqual(*theirs, ctx.Profile()) {
		ctx.putProfile(*theirs)
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
	s := ctx.Session()
	s.mu.Lock()
	if s.pendingProfiles == nil {
		s.pendingProfiles = map[string]bool{}
	}
	s.pendingProfiles[account] = true
	s.mu.Unlock()
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
	add("settings.startpage", func(p alborz.Profile) string {
		key := map[string]string{"calendar": "nav.calendar", "contacts": "nav.contacts", "tasks": "nav.tasks", "journal": "nav.journal"}[p.Reading.StartPage]
		if key == "" {
			key = "nav.email"
		}
		return g.T(key)
	})
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
	add("settings.attribution", func(p alborz.Profile) string { return onOff(!p.Writing.NoAttribution) })
	add("settings.poolsignatures", func(p alborz.Profile) string { return onOff(p.Writing.PoolSignatures) })
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
	add("settings.accountnames", func(p alborz.Profile) string { return onOff(!p.Display.HideAccountNames) })
	add("settings.status", func(p alborz.Profile) string {
		return selectedLabels(g, ctx.Server.Status, p.Display.Status, func(seg Segment) string { return seg.Name }, ".status")
	})
	add("settings.addons", func(p alborz.Profile) string {
		return selectedLabels(g, ctx.Server.Addons, p.Display.Addons, func(a Addon) string { return a.Name }, ".addon")
	})
	add("settings.place", func(p alborz.Profile) string { return or(p.Place.Name, "settings.placenone") })
	add("settings.phonebar", func(p alborz.Profile) string { return onOff(p.Display.PhoneBar) })
	add("settings.language", func(p alborz.Profile) string {
		return or(locale.Name(p.Display.Language), "settings.langauto")
	})
	a, b := theirs.Gestures.Resolved(), ours.Gestures.Resolved()
	for _, section := range []struct {
		label        string
		theirs, ours alborz.SwipeActions
	}{
		{"nav.email", a.Mail, b.Mail}, {"nav.tasks", a.Tasks, b.Tasks},
		{"nav.contacts", a.Contacts, b.Contacts}, {"nav.journal", a.Journal, b.Journal},
		{"events.search", a.Events, b.Events},
	} {
		for _, direction := range []struct{ label, theirs, ours string }{
			{"gestures.left", section.theirs.Left, section.ours.Left},
			{"gestures.right", section.theirs.Right, section.ours.Right},
		} {
			if direction.theirs != direction.ours {
				out = append(out, profileChange{
					g.T("gestures.title") + " · " + g.T(section.label) + " · " + g.T(direction.label),
					g.T(SwipeActionKey(direction.theirs)), g.T(SwipeActionKey(direction.ours)),
				})
			}
		}
	}
	return out
}

// SwipeActionKey is the label shared by gesture settings and profile-choice notices.
func SwipeActionKey(action string) string {
	switch action {
	case "delete":
		return "common.delete"
	case "archive":
		return "mailbox.archive"
	case "junk":
		return "mailbox.spam"
	case "complete":
		return "tasks.markcomplete"
	case "star":
		return "keys.star"
	}
	return "gestures.none"
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

// SettingsRail is the settings pages' rail: what this browser keeps, the lists merged over every account
// under MergedRows, then each account's pages.
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
	lists := []struct{ label, route string }{
		{"settings.signatures", "mail.signatures"}, {"settings.servers", "settings.servers"},
		{"sessions.title", "settings.sessions"}, {"notifications.title", "settings.notifications"},
	}
	list := func(on bool, q string) []RailRow {
		var out []RailRow
		for _, l := range lists {
			out = append(out, RailRow{Label: ctx.T(l.label), Href: ctx.Path(l.route) + q, Active: on && strings.HasPrefix(path, ctx.Path(l.route))})
		}
		return out
	}
	rows[MergedRows] = list(ctx.Unified, "")
	for _, account := range ctx.Accounts() {
		scoped := !ctx.Unified && ctx.Login != nil && account.Username == ctx.Login.Username()
		q := AccountQuery(account.Username)
		form := func(label, route string) RailRow {
			return RailRow{Label: ctx.T(label), Href: ctx.Path(route) + q, Active: scoped && path == ctx.Path(route)}
		}
		rows[account.Username] = append(append([]RailRow{form("settings.account", "mail.account")}, list(scoped, q)...),
			form("settings.password", "settings.password"), form("settings.data", "settings.data"), form("settings.encryption", "settings.encryption"))
	}
	return rows
}

// MergedRows keys a rail's rows under All accounts (settings, filters); no address is this.
const MergedRows = "*"

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
