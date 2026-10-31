package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	web.PageData[map[string][]web.RailRow]
	Theme string
	// Themes are the overlays on offer, built in or dropped in by a deployment.
	Themes           []web.Theme
	AlignByScript    bool
	PhoneBar         bool
	ShowAccountNames bool
	TextSize         string
	// Segments are the status bar's on offer, StatusOn those turned on.
	Segments []string
	StatusOn map[string]bool
	// Addons are the add-ons on offer, AddonsOn those turned on.
	Addons   []string
	AddonsOn map[string]bool
	// AddonScripts are the add-ons' settings scripts.
	AddonScripts []string

	Reading    alborz.Reading
	Writing    alborz.Writing
	MaxPerPage int
	// LockMinutes is how long this browser waits before it locks; zero without a passkey.
	LockMinutes int
	// Shown is the profile the form was drawn from, which a save compares the form against.
	Shown    string
	Gestures []GestureSetting
}

// GestureSetting offers the two physical swipe directions for one section.
type GestureSetting struct {
	Name, Label string
	Left, Right []GestureOption
}

// GestureOption is one supported action or the section's default.
type GestureOption struct {
	Value, Label string
	Selected     bool
}

// handleLanguage sets the language alone and returns to the page it was chosen on, without the URL's
// language, which would outrank the new one.
func handleLanguage(ctx *web.Context) error {
	to, err := url.Parse(ctx.NextOr(ctx.Path("mail.home")))
	if err != nil {
		return err
	}
	q := to.Query()
	q.Del("lang")
	to.RawQuery = web.Query(q)
	return ctx.SaveQuietRedirect(ctx.SetLanguage(ctx.FormValue("language")), to.String())
}

func handleProfile(ctx *web.Context) error {
	render := func(status int, message string, p alborz.Profile) error {
		shown, err := json.Marshal(p)
		if err != nil {
			return err
		}
		return ctx.Render(status, "settings/general", &ProfileRenderData{
			PageData:         *web.NewSettingsPageData(ctx, ctx.T("settings.general")).WithRefused(message),
			Theme:            p.Display.Theme,
			Themes:           ctx.Themes(),
			AlignByScript:    p.Display.AlignByScript,
			PhoneBar:         p.Display.PhoneBar,
			ShowAccountNames: !p.Display.HideAccountNames,
			TextSize:         p.Display.TextSize,
			Segments:         segments(ctx),
			StatusOn:         turnedOn(p.Display.Status),
			Addons:           addons(ctx),
			AddonsOn:         turnedOn(p.Display.Addons),
			AddonScripts:     addonScripts(ctx),
			Reading:          p.Reading,
			Writing:          p.Writing,
			MaxPerPage:       web.MaxMessagesPerPage,
			LockMinutes:      lockMinutes(ctx.Session().Lock()),
			Shown:            string(shown),
			Gestures:         gestureSettings(ctx, p.Gestures),
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
	if _, ok := given("shown"); ok {
		drawn = alborz.Profile{}
		if _, err := web.DecodeForm(ctx, "shown", &drawn, "the form's shown profile does not decode"); err != nil {
			return err
		}
	}
	p := drawn
	for _, field := range gestureFields(&p.Gestures) {
		if v, ok := given("swipe_" + field.name + "_left"); ok {
			field.pair.Left = v
		}
		if v, ok := given("swipe_" + field.name + "_right"); ok {
			field.pair.Right = v
		}
	}
	if !p.Gestures.Valid() {
		return render(http.StatusUnprocessableEntity, ctx.T("form.swipeaction"), shown)
	}
	reading := &p.Reading
	if v, ok := given("start_page"); ok {
		if !slices.Contains([]string{"", "mail", "calendar", "contacts", "tasks", "journal"}, v) {
			return render(http.StatusUnprocessableEntity, ctx.T("form.startpage"), shown)
		}
		reading.StartPage = v
	}
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
	if _, ok := sent["pool_signatures"]; ok || sent["writing_preferences"] != nil {
		p.Writing.PoolSignatures = ctx.FormValue("pool_signatures") != ""
	}
	if _, ok := sent["include_attribution"]; ok || sent["writing_preferences"] != nil {
		p.Writing.NoAttribution = ctx.FormValue("include_attribution") == ""
	}
	if v, ok := given("theme"); ok {
		p.Display.Theme = ""
		if slices.ContainsFunc(ctx.Themes(), func(t web.Theme) bool { return t.Name == v }) {
			p.Display.Theme = v
		}
	}
	if v, ok := given("language"); ok {
		p.Display.Language = ""
		if locale.Supported(v) {
			p.Display.Language = v
		}
	}
	if v, ok := given("text_size"); ok {
		p.Display.TextSize = ""
		if slices.Contains(web.TextSizes(), v) {
			p.Display.TextSize = v
		}
	}
	p.Display.AlignByScript = ctx.FormValue("align_script") != ""
	if _, ok := sent["show_account_names"]; ok || sent["account_preferences"] != nil {
		p.Display.HideAccountNames = ctx.FormValue("show_account_names") == ""
	}
	p.Display.Status = chosen(p.Display.Status, segments(ctx), sent["status"])
	p.Display.Addons = chosen(p.Display.Addons, addons(ctx), sent["addon"])
	p.Display.PhoneBar = ctx.FormValue("phone_bar") != ""
	var lockAfter time.Duration
	if v, ok := given("lock_minutes"); ok {
		minutes, err := locale.ReadInt(v)
		lockAfter = time.Duration(minutes) * time.Minute
		if err != nil || lockAfter < alborz.MinLockAfter {
			return render(http.StatusUnprocessableEntity, ctx.T("form.lockminutes"), shown)
		}
	}
	if err := ctx.UpdateProfile(func(stored *alborz.Profile) {
		web.KeepChanges(reflect.ValueOf(stored).Elem(), reflect.ValueOf(drawn), reflect.ValueOf(p))
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
	return ctx.NoticeRedirect(ctx.T("notice.settingssaved"), ctx.Path("settings.profile"))
}

type gestureField struct {
	name, label string
	pair        *alborz.SwipeActions
}

func gestureFields(g *alborz.Gestures) []gestureField {
	return []gestureField{
		{"mail", "nav.email", &g.Mail}, {"tasks", "nav.tasks", &g.Tasks},
		{"contacts", "nav.contacts", &g.Contacts}, {"journal", "nav.journal", &g.Journal},
		{"events", "events.search", &g.Events},
	}
}

func gestureSettings(ctx *web.Context, g alborz.Gestures) []GestureSetting {
	defaults := (alborz.Gestures{}).Resolved()
	defaultFields := gestureFields(&defaults)
	var settings []GestureSetting
	for i, field := range gestureFields(&g) {
		options := func(value, fallback string) []GestureOption {
			out := []GestureOption{{Label: fmt.Sprintf(ctx.T("gestures.default"), ctx.T(web.SwipeActionKey(fallback))), Selected: value == ""}}
			for _, action := range alborz.GestureActions(field.name) {
				out = append(out, GestureOption{Value: action, Label: ctx.T(web.SwipeActionKey(action)), Selected: value == action})
			}
			return out
		}
		settings = append(settings, GestureSetting{Name: field.name, Label: ctx.T(field.label),
			Left: options(field.pair.Left, defaultFields[i].pair.Left), Right: options(field.pair.Right, defaultFields[i].pair.Right)})
	}
	return settings
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
	return ctx.SaveRedirect(answer(login), fmt.Sprintf(ctx.T(done), account), ctx.Path("settings.profile"))
}

func segments(ctx *web.Context) []string {
	return offeredNames(ctx.Server.Status, func(s web.Segment) string { return s.Name })
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
	return offeredNames(ctx.Server.Addons, func(a web.Addon) string { return a.Name })
}

func offeredNames[T any](items []T, name func(T) string) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, name(item))
	}
	return names
}

func addonScripts(ctx *web.Context) []string {
	var paths []string
	for _, a := range ctx.Server.Addons {
		if a.Settings != "" {
			paths = append(paths, ctx.Path(a.Settings))
		}
	}
	return paths
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
