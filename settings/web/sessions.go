package web

import (
	"net/http"

	"git.mehdix.org/alborz/web"
)

// SessionsRenderData renders sessions.html.
type SessionsRenderData struct {
	web.PageData[map[string][]web.RailRow]
	Signed []web.Signed
}

func handleSessions(ctx *web.Context) error {
	account := ctx.Login.Username()
	if ctx.Request().Method == http.MethodPost {
		id := ctx.FormValue("session")
		if err := ctx.Server.Sessions.SignOut(id, account); err != nil {
			return err
		}
		return ctx.NoticeRedirect(ctx.T("notice.signedout"), ctx.URL("settings.sessions"))
	}
	signed, err := ctx.Server.Sessions.SignedIn(account, ctx.Session().ID)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "settings/sessions", &SessionsRenderData{
		PageData: *web.NewSettingsPageData(ctx, ctx.T("sessions.title")),
		Signed:   signed,
	})
}
