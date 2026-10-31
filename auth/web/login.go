package web

import (
	"cmp"
	"fmt"
	"net/http"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

func handleLogin(ctx *web.Context) error {
	if ctx.Server.NeedsSetup() {
		return ctx.QuietRedirect(ctx.Path("auth.setup"))
	}
	username := ctx.FormValue("username")
	password := ctx.FormValue("password")
	remember := ctx.FormValue("remember-me")
	add := ctx.QueryParam("add") == "1"

	title := "login.short"
	if add {
		title = "login.add"
	}
	renderData := struct {
		web.BaseRenderData
		RememberDays int
		Remember     bool
		Add          bool
		Username     string
	}{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T(title)),
		Username:       username,
		RememberDays:   int(alborz.CredentialCookieLife().Hours() / 24),
		Remember:       remember == "on",
		Add:            add,
	}
	render := func(status int) error { return ctx.Render(status, "login", &renderData) }

	// Remembered credentials re-login existing accounts; they cannot add one.
	if username == "" && password == "" && !add && ctx.RestoreRememberedAccounts() {
		return loginRedirect(ctx)
	}

	if ctx.Request().Method == http.MethodPost && (username == "" || password == "") {
		renderData.GlobalData.Notice = &web.Notice{Kind: web.NoticeFailed, Text: ctx.T("notice.loginmissing")}
		return render(http.StatusUnprocessableEntity)
	}

	if ctx.Request().Method == http.MethodPost && username != "" && password != "" {
		s, err := ctx.Server.SignIn(username, password, ctx.RealIP())
		if err != nil {
			// One line per refusal with the client address, for fail2ban.
			ctx.Logger().Printf("Login failed for %q from %s: %v", username, ctx.RealIP(), err)
			text, status := ctx.SignInRefusal(err)
			if text == "" {
				return fmt.Errorf("failed to put connection in pool: %w", err)
			}
			renderData.GlobalData.Notice = &web.Notice{Kind: web.NoticeFailed, Text: text}
			return render(status)
		}
		ctx.AddAccount(s)

		if remember == "on" {
			if err := ctx.SetLoginToken(username, password); err != nil {
				return err
			}
		}

		return loginRedirect(ctx)
	}

	return render(http.StatusOK)
}

// Being signed in is what the next page shows, wherever it is.
func loginRedirect(ctx *web.Context) error {
	to := ctx.DefaultPath()
	if next := ctx.NextOr(""); next != "" && next != ctx.Path("auth.login") {
		to = next
	}
	to, err := ctx.OfferTour(to)
	if err != nil {
		return err
	}
	// A boosted body swap keeps the public head, which has no session guard.
	if ctx.Partial() {
		ctx.Quiet()
		return ctx.Relocate(to)
	}
	return ctx.QuietRedirect(to)
}

// handleLogout signs out the `account` sent, the request's own without one, or every account with `all`.
func handleLogout(ctx *web.Context) error {
	usernames := []string{cmp.Or(ctx.FormValue("account"), ctx.Login.Username())}
	if ctx.FormValue("all") != "" {
		usernames = nil
		for _, a := range ctx.Accounts() {
			usernames = append(usernames, a.Username)
		}
	}
	ctx.Quiet()
	var left *alborz.Login
	for _, username := range usernames {
		if ctx.LoginFor(username) == nil {
			return web.NewError(http.StatusBadRequest, "not signed in to that account")
		}
		ctx.Server.ForgetAccount(username)
		left = ctx.LogoutAccount(username)
	}
	to := ctx.Path("auth.login")
	if left != nil {
		to = ctx.Path("mail.mailbox", "INBOX")
	}
	if ctx.Partial() {
		return ctx.Relocate(to)
	}
	return ctx.FoundRedirect(to)
}
