package auth

import (
	"fmt"
	"net/http"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

func handleLogin(ctx *web.Context) error {
	username := ctx.FormValue("username")
	password := ctx.FormValue("password")
	remember := ctx.FormValue("remember-me")
	add := ctx.QueryParam("add") == "1"

	renderData := struct {
		web.BaseRenderData
		RememberDays int
		Add          bool
		Username     string
	}{
		BaseRenderData: *web.NewBaseRenderData(ctx),
		Username:       username,
		RememberDays:   int(alborz.CredentialCookieLife().Hours() / 24),
		Add:            add,
	}
	if add {
		renderData.BaseRenderData.WithTitle(ctx.T("login.add"))
	} else {
		renderData.BaseRenderData.WithTitle(ctx.T("login.short"))
	}

	// Remembered credentials re-login existing accounts; they cannot add one.
	if username == "" && password == "" && !add && ctx.RestoreRememberedAccounts() {
		return loginRedirect(ctx)
	}

	if ctx.Request().Method == http.MethodPost && (username == "" || password == "") {
		renderData.BaseRenderData.GlobalData.Notice = &web.Notice{Kind: web.NoticeFailed, Text: ctx.T("notice.loginmissing")}
		return ctx.Render(http.StatusUnprocessableEntity, "login", &renderData)
	}

	if username != "" && password != "" {
		s, err := ctx.Server.SignIn(username, password, ctx.RealIP())
		if err != nil {
			// One line per refusal with the client address, for fail2ban.
			ctx.Logger().Printf("Login failed for %q from %s: %v", username, ctx.RealIP(), err)
			text, status := ctx.SignInRefusal(err)
			if text == "" {
				return fmt.Errorf("failed to put connection in pool: %w", err)
			}
			renderData.BaseRenderData.GlobalData.Notice = &web.Notice{Kind: web.NoticeFailed, Text: text}
			return ctx.Render(status, "login", &renderData)
		}
		ctx.AddAccount(s)

		if remember == "on" {
			ctx.SetLoginToken(username, password)
		}

		return loginRedirect(ctx)
	}

	return ctx.Render(http.StatusOK, "login", &renderData)
}

func loginRedirect(ctx *web.Context) error {
	// A second leading slash or backslash is scheme-relative: off-site.
	if path := ctx.QueryParam("next"); path != "" && path[0] == '/' && path != ctx.Path("auth.login") &&
		!strings.HasPrefix(path, "//") && !strings.HasPrefix(path, "/\\") {
		return ctx.Redirect(http.StatusFound, path)
	}
	ctx.Quiet()
	return ctx.Redirect(http.StatusFound, ctx.Path("mail.mailbox", "INBOX"))
}

func HandleLogout(ctx *web.Context) error {
	username := ctx.Login.Username()
	if ctx.FormValue("account") != "" {
		username = ctx.FormValue("account")
	}
	target := ctx.LoginFor(username)
	if target == nil {
		return web.NewError(http.StatusBadRequest, "not signed in to that account")
	}
	ctx.Server.ForgetAccount(username)
	ctx.Quiet()
	if ctx.LogoutAccount(username) != nil {
		return ctx.Redirect(http.StatusFound, ctx.Path("mail.mailbox", "INBOX"))
	}
	return ctx.Redirect(http.StatusFound, ctx.Path("auth.login"))
}
