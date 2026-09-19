package alborzbase

import (
	"net/http"

	"git.mehdix.org/alborz"
)

// SessionsRenderData renders sessions.html: the browsers signed into
// the account, the one asking among them.
type SessionsRenderData struct {
	alborz.BaseRenderData
	Rail    map[string][]alborz.RailRow
	Signed  []alborz.Signed
	Account string
}

// handleSessions lists the browsers signed into the account and signs
// one of them out of it, which a lost phone or a borrowed laptop needs
// from somewhere else.
func handleSessions(ctx *alborz.Context) error {
	account := ctx.Session.Username()
	if ctx.Request().Method == http.MethodPost {
		id := ctx.FormValue("visit")
		// The browser asking signs out the way it always does, which
		// also ends its stay when it was the last account.
		if id == ctx.Visit().ID {
			return handleLogout(ctx)
		}
		if err := ctx.Server.Visits.SignOut(id, account); err != nil {
			return err
		}
		ctx.PutNotice(ctx.T("notice.signedout"))
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings/sessions"))
	}
	signed, err := ctx.Server.Visits.SignedIn(account, ctx.Visit().ID)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "sessions.html", &SessionsRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("sessions.title")),
		Rail:           settingsRail(ctx),
		Signed:         signed,
		Account:        account,
	})
}
