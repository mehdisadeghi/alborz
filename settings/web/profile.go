package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/calsys"
	"git.mehdix.org/alborz/internal/locale"
	"git.mehdix.org/alborz/web"
)

// ProfileRenderData is the settings page that belongs to no account.
type ProfileRenderData struct {
	web.BaseRenderData
	Rail  map[string][]web.RailRow
	Theme string
	// Themes are the overlays on offer, built in or dropped in by a deployment.
	Themes        []web.Theme
	AlignByScript bool
	TextSize      string
	// Segments are the status bar's on offer, StatusOn those turned on.
	Segments []string
	StatusOn map[string]bool
	// Addons are the add-ons on offer, AddonsOn those turned on.
	Addons   []string
	AddonsOn map[string]bool

	Reading    alborz.Reading
	Writing    alborz.Writing
	MaxPerPage int
	// LockMinutes is how long this browser waits before it locks; zero without a passkey.
	LockMinutes int
	// Shown is the profile the form was drawn from, which a save compares the form against.
	Shown string
}

func handleLanguage(ctx *web.Context) error {
	if err := ctx.SetLanguage(ctx.FormValue("language")); err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.SaveRefusal(err)})
	} else {
		ctx.Quiet()
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.Path("mail.home")))
}

func handleProfile(ctx *web.Context) error {
	render := func(status int, message string, p alborz.Profile) error {
		shown, err := json.Marshal(p)
		if err != nil {
			return err
		}
		return ctx.Render(status, "settings/general", &ProfileRenderData{
			BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.general")).Refused(message),
			Rail:           web.SettingsRail(ctx),
			Theme:          p.Display.Theme,
			Themes:         ctx.Themes(),
			AlignByScript:  p.Display.AlignByScript,
			TextSize:       p.Display.TextSize,
			Segments:       segments(ctx),
			StatusOn:       turnedOn(p.Display.Status),
			Addons:         addons(ctx),
			AddonsOn:       turnedOn(p.Display.Addons),
			Reading:        p.Reading,
			Writing:        p.Writing,
			MaxPerPage:     web.MaxMessagesPerPage,
			LockMinutes:    lockMinutes(ctx.Session().Lock()),
			Shown:          string(shown),
		})
	}
	shown := ctx.Profile()
	shown.Reading = ctx.Reading()
	shown.Display.Theme, shown.Display.TextSize = ctx.Theme(), ctx.TextSize()
	if ctx.Request().Method != http.MethodPost {
		return render(http.StatusOK, "", shown)
	}

	// An absent field keeps its value; a present empty one clears, except a checkbox, which sends nothing for off.
	sent, err := ctx.FormParams()
	if err != nil {
		return err
	}
	given := func(name string) (string, bool) {
		values, ok := sent[name]
		if !ok || len(values) == 0 {
			return "", false
		}
		return values[0], true
	}

	// What the form changed is what it sends that differs from what it showed; the rest stays as stored.
	drawn := shown
	if v, ok := given("shown"); ok {
		drawn = alborz.Profile{}
		if err := json.Unmarshal([]byte(v), &drawn); err != nil {
			return web.NewError(http.StatusBadRequest, "the form's shown profile does not decode")
		}
	}
	p := drawn
	reading := &p.Reading
	if v, ok := given("messages_per_page"); ok {
		reading.MessagesPerPage, err = locale.ReadInt(v)
		if err != nil || reading.MessagesPerPage <= 0 || reading.MessagesPerPage > web.MaxMessagesPerPage {
			return render(http.StatusUnprocessableEntity, fmt.Sprintf(ctx.T("form.perpage"), web.MaxMessagesPerPage), shown)
		}
	}
	if v, ok := given("timezone"); ok {
		reading.Timezone = v
	}
	reading.PreferHTML = ctx.FormValue("prefer_html") != ""
	if fdow, ok := given("first_day_of_week"); ok && fdow != "" {
		reading.FirstDayOfWeek, err = locale.ReadInt(fdow)
		if err != nil || reading.FirstDayOfWeek < 1 || reading.FirstDayOfWeek > 7 {
			return render(http.StatusUnprocessableEntity, ctx.T("form.firstday"), shown)
		}
	}
	if v, ok := given("calendar"); ok {
		reading.Primary = calsys.Known(v)
	}
	if v, ok := given("secondary"); ok {
		reading.Secondary = calsys.Known(v)
	}
	if reading.Secondary == calsys.Of(reading.Primary).Name() {
		reading.Secondary = ""
	}
	if v, ok := given("reply_position"); ok {
		p.Writing.ReplyBelowQuote = v == "below"
	}
	p.Writing.NoDirectionPart = ctx.FormValue("direction_part") == ""
	if v, ok := given("theme"); ok {
		p.Display.Theme = ""
		if slices.ContainsFunc(ctx.Themes(), func(t web.Theme) bool { return t.Name == v }) {
			p.Display.Theme = v
		}
	}
	if v, ok := given("text_size"); ok {
		p.Display.TextSize = ""
		if slices.Contains(web.TextSizes(), v) {
			p.Display.TextSize = v
		}
	}
	p.Display.AlignByScript = ctx.FormValue("align_script") != ""
	p.Display.Status = chosen(p.Display.Status, segments(ctx), sent["status"])
	p.Display.Addons = chosen(p.Display.Addons, addons(ctx), sent["addon"])
	var lockAfter time.Duration
	if v, ok := given("lock_minutes"); ok {
		minutes, err := locale.ReadInt(v)
		lockAfter = time.Duration(minutes) * time.Minute
		if err != nil || lockAfter < alborz.MinLockAfter {
			return render(http.StatusUnprocessableEntity, ctx.T("form.lockminutes"), shown)
		}
	}
	if err := ctx.UpdateProfile(func(stored *alborz.Profile) {
		keepChanges(reflect.ValueOf(stored).Elem(), reflect.ValueOf(drawn), reflect.ValueOf(p))
	}); err != nil {
		return render(http.StatusUnprocessableEntity, ctx.SaveRefusal(err), p)
	}
	if lockAfter > 0 {
		session := ctx.Session()
		session.SetLockAfter(lockAfter)
		if err := ctx.Server.Sessions.Save(session); err != nil {
			return err
		}
	}
	ctx.PutNotice(ctx.T("notice.settingssaved"))
	return ctx.Redirect(http.StatusFound, ctx.Path("settings.profile"))
}

func handleProfileTake(ctx *web.Context) error {
	return answerProfile(ctx, ctx.TakeProfile, "profile.taken")
}

func handleProfileDiscard(ctx *web.Context) error {
	return answerProfile(ctx, ctx.KeepProfile, "profile.discarded")
}

func answerProfile(ctx *web.Context, answer func(*alborz.Login) error, done string) error {
	account := ctx.FormValue("account")
	login := ctx.LoginFor(account)
	if login == nil {
		return web.NewError(http.StatusBadRequest, "not signed in to that account")
	}
	if err := answer(login); err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.SaveRefusal(err)})
	} else {
		ctx.PutNotice(fmt.Sprintf(ctx.T(done), account))
	}
	return ctx.Redirect(http.StatusFound, ctx.Path("settings.profile"))
}

// keepChanges sets on stored each field mine changed from base, and a list by the names added and taken
// away, so a field the save did not change stays as another save left it.
func keepChanges(stored, base, mine reflect.Value) {
	for i := range base.NumField() {
		s, b, m := stored.Field(i), base.Field(i), mine.Field(i)
		switch {
		case b.Kind() == reflect.Struct:
			keepChanges(s, b, m)
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

func segments(ctx *web.Context) []string {
	var names []string
	for _, s := range ctx.Server.Status {
		names = append(names, s.Name)
	}
	return names
}

// chosen are the names sent among those offered; one not offered was not on the page, so it stays as it was.
func chosen(was, offered, sent []string) []string {
	out := slices.DeleteFunc(slices.Clone(was), func(name string) bool { return slices.Contains(offered, name) })
	for _, name := range sent {
		if slices.Contains(offered, name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

func addons(ctx *web.Context) []string {
	var names []string
	for _, a := range ctx.Server.Addons {
		names = append(names, a.Name)
	}
	return names
}

func turnedOn(names []string) map[string]bool {
	on := map[string]bool{}
	for _, name := range names {
		on[name] = true
	}
	return on
}

func lockMinutes(l alborz.Lock) int {
	if len(l.Passkeys) == 0 {
		return 0
	}
	return int(l.Wait() / time.Minute)
}
