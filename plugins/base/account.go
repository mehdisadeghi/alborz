package alborzbase

import (
	"errors"
	"fmt"
	"net"
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

	if username != "" && password != "" {
		s, err := ctx.Server.Sessions.Put(username, password)
		if err != nil {
			ctx.Logger().Printf("Login failed for %q: %v", username, err)
			if _, ok := err.(alborz.AuthError); ok {
				renderData.BaseRenderData.GlobalData.Notice = &alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.T("notice.loginfailed")}
				return ctx.Render(http.StatusUnauthorized, "login.html", &renderData)
			}
			var domainErr alborz.UnknownDomainError
			if errors.As(err, &domainErr) {
				// Which domain, and in the reader's own language: the
				// error's own words are English and are for the log.
				text := ctx.T("login.needsdomain")
				if domainErr.Domain != "" {
					text = fmt.Sprintf(ctx.T("login.baddomain"), domainErr.Domain)
				}
				renderData.BaseRenderData.GlobalData.Notice = &alborz.Notice{Kind: alborz.NoticeFailed, Text: text}
				return ctx.Render(http.StatusUnauthorized, "login.html", &renderData)
			}
			var baseline alborz.BaselineError
			if errors.As(err, &baseline) {
				renderData.BaseRenderData.GlobalData.Notice = &alborz.Notice{Kind: alborz.NoticeFailed, Text: fmt.Sprintf(ctx.T("notice.loginerror"), baseline.Error())}
				return ctx.Render(http.StatusBadGateway, "login.html", &renderData)
			}
			var netErr *net.OpError
			if errors.As(err, &netErr) {
				renderData.BaseRenderData.GlobalData.Notice = &alborz.Notice{Kind: alborz.NoticeFailed, Text: fmt.Sprintf(ctx.T("notice.loginerror"), netErr.Err)}
				return ctx.Render(http.StatusServiceUnavailable, "login.html", &renderData)
			}
			return fmt.Errorf("failed to put connection in pool: %v", err)
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
