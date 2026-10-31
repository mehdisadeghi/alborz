package web

import (
	"net/http"

	"git.mehdix.org/alborz/web"
)

// SessionsRenderData renders sessions.html.
type SessionsRenderData struct {
	web.PageData[map[string][]web.RailRow]
	Signed []SignedRow
}

// SignedRow is a session as the list shows it, with the account signed in there.
type SignedRow struct {
	web.Signed
	Account string
}

func handleSessions(ctx *web.Context) error {
	if ctx.Request().Method == http.MethodPost {
		if err := ctx.Server.Sessions.SignOut(ctx.FormValue("session"), ctx.Login.Username()); err != nil {
			return err
		}
		return ctx.NoticeRedirect(ctx.T("notice.signedout"), ctx.NextOr(ctx.URL("settings.sessions")))
	}
	data := &SessionsRenderData{PageData: *web.NewSettingsPageData(ctx, ctx.T("sessions.title"))}
	for _, login := range ctx.InScope() {
		signed, err := ctx.Server.Sessions.SignedIn(login.Username(), ctx.Session().ID)
		if err != nil {
			return err
		}
		for _, s := range signed {
			data.Signed = append(data.Signed, SignedRow{Signed: s, Account: login.Username()})
		}
	}
	return ctx.Render(http.StatusOK, "settings/sessions", data)
}
