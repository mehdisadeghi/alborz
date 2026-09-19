package alborzbase

import (
	"fmt"
	"net/http"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/labstack/echo/v4"
)

func handleLogin(ctx *alborz.Context) error {
	username := ctx.FormValue("username")
	password := ctx.FormValue("password")
	remember := ctx.FormValue("remember-me")
	add := ctx.QueryParam("add") == "1"

	renderData := struct {
		alborz.BaseRenderData
		CanRememberMe bool
		RememberDays  int
		Add           bool
		// Username is what was typed, kept when the form comes back
		// refused: a rejected sign-in should not cost the address too.
		Username string
	}{
		BaseRenderData: *alborz.NewBaseRenderData(ctx),
		Username:       username,
		CanRememberMe:  ctx.Server.Visits.Remembers(),
		// The label says how long the box keeps you signed in, read
		// from the lifetime the credential cookie is actually given.
		RememberDays: int(alborz.CredentialCookieLife().Hours() / 24),
		Add:          add,
	}
	if add {
		renderData.BaseRenderData.WithTitle(ctx.T("login.add"))
	} else {
		renderData.BaseRenderData.WithTitle(ctx.T("login.short"))
	}

	// The remembered credentials would re-login the accounts the user
	// already has, not add a new one.
	if username == "" && password == "" && !add && ctx.RestoreRememberedAccounts() {
		return loginRedirect(ctx)
	}

	// A form that comes back as it was left, saying nothing, reads as if
	// nothing was sent.
	if ctx.Request().Method == http.MethodPost && (username == "" || password == "") {
		renderData.BaseRenderData.GlobalData.Notice = &alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.T("notice.loginmissing")}
		return ctx.Render(http.StatusUnprocessableEntity, "login.html", &renderData)
	}

	if username != "" && password != "" {
		s, err := ctx.Server.SignIn(username, password, ctx.RealIP())
		if err != nil {
			// One line per refusal naming the reader's address, for a
			// fail2ban filter on alborz's own log.
			ctx.Logger().Printf("Login failed for %q from %s: %v", username, ctx.RealIP(), err)
			text, status := ctx.SignInRefusal(err)
			if text == "" {
				return fmt.Errorf("failed to put connection in pool: %w", err)
			}
			renderData.BaseRenderData.GlobalData.Notice = &alborz.Notice{Kind: alborz.NoticeFailed, Text: text}
			return ctx.Render(status, "login.html", &renderData)
		}
		ctx.AddAccount(s)

		if remember == "on" {
			ctx.SetLoginToken(username, password)
		}

		return loginRedirect(ctx)
	}

	return ctx.Render(http.StatusOK, "login.html", &renderData)
}

// loginRedirect honors the next parameter after a successful login.
func loginRedirect(ctx *alborz.Context) error {
	// A second leading slash or backslash would make the target
	// scheme-relative, redirecting off-site.
	if path := ctx.QueryParam("next"); path != "" && path[0] == '/' && path != "/login" &&
		!strings.HasPrefix(path, "//") && !strings.HasPrefix(path, "/\\") {
		return ctx.Redirect(http.StatusFound, path)
	}
	return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
}

func handleLogout(ctx *alborz.Context) error {
	username := ctx.Session.Username()
	if ctx.FormValue("account") != "" {
		username = ctx.FormValue("account")
	}
	target := ctx.SessionFor(username)
	if target == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
	}
	ctx.Server.ForgetAccount(username)
	if ctx.LogoutAccount(username) != nil {
		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	}
	return ctx.Redirect(http.StatusFound, "/login")
}
