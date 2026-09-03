package alborzbase

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
		Add           bool
	}{
		BaseRenderData: *alborz.NewBaseRenderData(ctx),
		CanRememberMe:  ctx.Server.Options.LoginKey != nil,
		Add:            add,
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
				renderData.BaseRenderData.GlobalData.Notice = &alborz.Notice{Kind: alborz.NoticeFailed, Text: fmt.Sprintf(ctx.T("notice.loginerror"), domainErr.Error())}
				return ctx.Render(http.StatusUnauthorized, "login.html", &renderData)
			}
			var netErr *net.OpError
			if errors.As(err, &netErr) {
				renderData.BaseRenderData.GlobalData.Notice = &alborz.Notice{Kind: alborz.NoticeFailed, Text: fmt.Sprintf(ctx.T("notice.loginerror"), netErr.Err)}
				return ctx.Render(http.StatusServiceUnavailable, "login.html", &renderData)
			}
			return fmt.Errorf("failed to put connection in pool: %v", err)
		}
		ctx.AddAccount(s)
		// Follow the account from here on, so mail that arrives while
		// nobody is looking is noticed rather than waited for.
		Watch(s, ctx.Logger())

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
	if ctx.Request().Method == http.MethodPost && ctx.FormValue("account") != "" {
		username = ctx.FormValue("account")
	}
	target := ctx.SessionFor(username)
	if target == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
	}
	wasCurrent := target == ctx.DefaultSession
	ctx.Server.ForgetAccount(username)
	if next := ctx.LogoutAccount(username); next != nil {
		if wasCurrent {
			next.PutNotice(fmt.Sprintf(ctx.T("notice.signedinas"), next.Username()))
		}
		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	}
	return ctx.Redirect(http.StatusFound, "/login")
}

// switchDestination keeps a scope change on the page it was made from.
// Pooled sections exist under every scope; the account-scoped ones only
// outside the merged view. Deeper paths name an object of the account
// being left, so they return to the inbox, as does a missing or foreign
// next. The query goes with the old scope and is dropped.
func switchDestination(next string, unified bool) string {
	u, err := url.Parse(next)
	if err != nil || !strings.HasPrefix(u.Path, "/") || u.Host != "" {
		return "/mailbox/INBOX"
	}
	switch u.Path {
	case "/calendar", "/contacts", "/tasks":
		return u.Path
	case "/filters", "/settings":
		if !unified {
			return u.Path
		}
	}
	return "/mailbox/INBOX"
}

func handleSwitch(ctx *alborz.Context) error {
	unified := ctx.FormValue("account") == "unified"
	destination := switchDestination(ctx.FormValue("next"), unified)
	if unified {
		ctx.SetUnified(true)
		return ctx.Redirect(http.StatusFound, destination)
	}
	ctx.SetUnified(false)
	if !ctx.SwitchAccount(ctx.FormValue("account")) {
		ctx.Session.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("notice.expired")})
		return ctx.Redirect(http.StatusFound, "/login?add=1")
	}
	return ctx.Redirect(http.StatusFound, destination)
}
