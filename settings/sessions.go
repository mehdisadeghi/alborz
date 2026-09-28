package settings

import (
	"net/http"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
)

// SessionsRenderData renders sessions.html.
type SessionsRenderData struct {
	alborz.BaseRenderData
	Rail    map[string][]alborz.RailRow
	Signed  []alborz.Signed
	Account string
}

func handleSessions(ctx *alborz.Context) error {
	account := ctx.Login.Username()
	if ctx.Request().Method == http.MethodPost {
		id := ctx.FormValue("session")
		if id == ctx.Session().ID {
			return mail.HandleLogout(ctx)
		}
		if err := ctx.Server.Sessions.SignOut(id, account); err != nil {
			return err
		}
		ctx.PutNotice(ctx.T("notice.signedout"))
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings/sessions"))
	}
	signed, err := ctx.Server.Sessions.SignedIn(account, ctx.Session().ID)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "settings/sessions", &SessionsRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("sessions.title")),
		Rail:           mail.SettingsRail(ctx),
		Signed:         signed,
		Account:        account,
	})
}
