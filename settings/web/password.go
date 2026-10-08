package web

import (
	"errors"
	"fmt"
	"net/http"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

func handlePassword(ctx *web.Context) error {
	account := ctx.Login.Username()
	render := func(status int, alert string) error {
		return ctx.Render(status, "settings/password", web.NewSettingsPageData(ctx, ctx.T("settings.password")).WithRefused(alert))
	}
	unsupported := fmt.Sprintf(ctx.T("form.passwordunsupported"), ctx.Login.Domain())
	if ctx.Request().Method != http.MethodPost {
		if !ctx.Server.PasswdEnabled(ctx.Login.Domain()) {
			return render(http.StatusOK, unsupported)
		}
		return render(http.StatusOK, "")
	}

	next := ctx.FormValue("new")
	if next != ctx.FormValue("confirm") {
		return render(http.StatusUnprocessableEntity, ctx.T("form.passwordmismatch"))
	}
	err := ctx.Server.ChangePassword(ctx.Request().Context(), ctx.Login, ctx.FormValue("current"), next, ctx.RealIP())
	var refusal alborz.PasswordRefusal
	var upstream alborz.UpstreamError
	var paused alborz.PausedError
	switch {
	case errors.Is(err, errors.ErrUnsupported):
		return render(http.StatusUnprocessableEntity, unsupported)
	case errors.As(err, &paused):
		text, status := ctx.SignInRefusal(err)
		return render(status, text)
	case errors.As(err, &refusal):
		// One line per refusal with the client address, as signing in logs, for fail2ban.
		ctx.Logger().Printf("Password change refused for %q from %s: %v", account, ctx.RealIP(), err)
		return render(http.StatusUnprocessableEntity, fmt.Sprintf(ctx.T("form.passwordrefused"), refusal.Text))
	case errors.As(err, &upstream):
		ctx.Logger().Printf("Password change for %q: %v", account, err)
		return render(http.StatusBadGateway, ctx.T("form.passwordnoanswer"))
	case err != nil:
		return err
	}

	login, err := ctx.Server.SignIn(account, next, ctx.RealIP())
	if err != nil {
		ctx.Logger().Printf("Signing in %q with the changed password: %v", account, err)
		why, _ := ctx.SignInRefusal(err)
		if why == "" {
			why = err.Error()
		}
		// The login in place goes on until it needs the password again, as after a change made elsewhere.
		return render(http.StatusBadGateway, fmt.Sprintf(ctx.T("form.passwordsigninfailed"), why))
	}
	if err := ctx.PasswordChanged(login, next); err != nil {
		return err
	}
	return ctx.NoticeRedirect(ctx.T("notice.passwordchanged"), ctx.URL("settings.password"))
}
