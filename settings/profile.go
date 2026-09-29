package settings

import (
	"fmt"
	"net/http"
	"slices"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/calsys"
	"git.mehdix.org/alborz/mail"
	"github.com/labstack/echo/v4"
)

// ProfileRenderData is the settings page that belongs to no account.
type ProfileRenderData struct {
	alborz.BaseRenderData
	Rail  map[string][]alborz.RailRow
	Theme string
	// Themes are the overlays on offer, built in or dropped in by a deployment.
	Themes        []alborz.Theme
	AlignByScript bool
	TextSize      string

	Reading    alborz.Reading
	Writing    alborz.Writing
	MaxPerPage int
	// LockMinutes is how long this browser waits before it locks; zero without a passkey.
	LockMinutes int
}

func handleLanguage(ctx *alborz.Context) error {
	if err := ctx.SetLanguage(ctx.FormValue("language")); err != nil {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.SaveRefusal(err)})
	} else {
		ctx.Quiet()
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr("/"))
}

func handleProfile(ctx *alborz.Context) error {
	render := func(status int, message string, p alborz.Profile) error {
		return ctx.Render(status, "settings/general", &ProfileRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("settings.general")).Refused(message),
			Rail:           mail.SettingsRail(ctx),
			Theme:          p.Display.Theme,
			Themes:         ctx.Themes(),
			AlignByScript:  p.Display.AlignByScript,
			TextSize:       p.Display.TextSize,
			Reading:        p.Reading,
			Writing:        p.Writing,
			MaxPerPage:     mail.MaxMessagesPerPage,
			LockMinutes:    lockMinutes(ctx.Session().Lock()),
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

	p := shown
	reading := &p.Reading
	if v, ok := given("messages_per_page"); ok {
		reading.MessagesPerPage, err = alborz.ReadInt(v)
		if err != nil || reading.MessagesPerPage <= 0 || reading.MessagesPerPage > mail.MaxMessagesPerPage {
			return render(http.StatusUnprocessableEntity, fmt.Sprintf(ctx.T("form.perpage"), mail.MaxMessagesPerPage), shown)
		}
	}
	if v, ok := given("timezone"); ok {
		reading.Timezone = v
	}
	reading.PreferHTML = ctx.FormValue("prefer_html") != ""
	if fdow, ok := given("first_day_of_week"); ok && fdow != "" {
		reading.FirstDayOfWeek, err = alborz.ReadInt(fdow)
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
	if reading.Secondary == reading.Primary {
		reading.Secondary = ""
	}
	if v, ok := given("reply_position"); ok {
		p.Writing.ReplyBelowQuote = v == "below"
	}
	p.Writing.NoDirectionPart = ctx.FormValue("direction_part") == ""
	if v, ok := given("theme"); ok {
		p.Display.Theme = ""
		if slices.ContainsFunc(ctx.Themes(), func(t alborz.Theme) bool { return t.Name == v }) {
			p.Display.Theme = v
		}
	}
	if v, ok := given("text_size"); ok {
		p.Display.TextSize = ""
		if slices.Contains(alborz.TextSizes(), v) {
			p.Display.TextSize = v
		}
	}
	p.Display.AlignByScript = ctx.FormValue("align_script") != ""
	var lockAfter time.Duration
	if v, ok := given("lock_minutes"); ok {
		minutes, err := alborz.ReadInt(v)
		lockAfter = time.Duration(minutes) * time.Minute
		if err != nil || lockAfter < alborz.MinLockAfter {
			return render(http.StatusUnprocessableEntity, ctx.T("form.lockminutes"), shown)
		}
	}
	if err := ctx.SetProfile(p); err != nil {
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
	return ctx.Redirect(http.StatusFound, "/settings")
}

func handleProfileTake(ctx *alborz.Context) error {
	return answerProfile(ctx, ctx.TakeProfile, "profile.taken")
}

func handleProfileDiscard(ctx *alborz.Context) error {
	return answerProfile(ctx, ctx.KeepProfile, "profile.discarded")
}

func answerProfile(ctx *alborz.Context, answer func(*alborz.Login) error, done string) error {
	account := ctx.FormValue("account")
	login := ctx.LoginFor(account)
	if login == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
	}
	if err := answer(login); err != nil {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.SaveRefusal(err)})
	} else {
		ctx.PutNotice(fmt.Sprintf(ctx.T(done), account))
	}
	return ctx.Redirect(http.StatusFound, "/settings")
}

func lockMinutes(l alborz.Lock) int {
	if len(l.Passkeys) == 0 {
		return 0
	}
	return int(l.Wait() / time.Minute)
}
