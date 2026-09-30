package web

import (
	"net/http"

	"git.mehdix.org/alborz/web"
)

// SessionsRenderData renders sessions.html.
type SessionsRenderData struct {
	web.BaseRenderData
	Rail    map[string][]web.RailRow
	Signed  []web.Signed
	Account string
}

func handleSessions(ctx *web.Context) error {
	account := ctx.Login.Username()
	if ctx.Request().Method == http.MethodPost {
		id := ctx.FormValue("session")
		if err := ctx.Server.Sessions.SignOut(id, account); err != nil {
			return err
		}
		ctx.PutNotice(ctx.T("notice.signedout"))
		return ctx.Redirect(http.StatusFound, ctx.URL("settings.sessions"))
	}
	signed, err := ctx.Server.Sessions.SignedIn(account, ctx.Session().ID)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "settings/sessions", &SessionsRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("sessions.title")),
		Rail:           web.SettingsRail(ctx),
		Signed:         signed,
		Account:        account,
	})
}
