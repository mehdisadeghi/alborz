package settings

import (
	"net/http"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
)

// PasskeysRenderData renders passkeys.html.
type PasskeysRenderData struct {
	web.BaseRenderData
	Rail     map[string][]web.RailRow
	Passkeys []alborz.Passkey
}

// PasskeyRenderData renders passkey-create.html.
type PasskeyRenderData struct {
	web.BaseRenderData
	Rail map[string][]web.RailRow
}

func registerPasskeyRoutes(g *web.Group) {
	g.GET("passkeys", "/settings/passkeys", func(ctx *web.Context) error {
		return ctx.Render(http.StatusOK, "settings/passkeys", &PasskeysRenderData{
			BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("passkeys.title")),
			Rail:           mail.SettingsRail(ctx),
			Passkeys:       ctx.Session().Lock().Passkeys,
		})
	})
	passkeyForm := func(ctx *web.Context) error {
		data := &PasskeyRenderData{
			BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("passkeys.new")),
			Rail:           mail.SettingsRail(ctx),
		}
		// Only a form without script posts here, and without script there is no passkey prompt.
		if ctx.Request().Method == http.MethodPost {
			data.Refused(ctx.T("passkeys.needsscript"))
			return ctx.Render(http.StatusUnprocessableEntity, "settings/passkey-create", data)
		}
		return ctx.Render(http.StatusOK, "settings/passkey-create", data)
	}
	g.GET("passkeys.create", "/settings/passkeys/create", passkeyForm)
	g.POST("passkeys.create", "/settings/passkeys/create", passkeyForm)
	g.POST("passkeys.begin", "/settings/passkeys/begin", func(ctx *web.Context) error {
		options, err := ctx.BeginPasskey()
		if err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, options)
	})
	g.POST("passkeys.finish", "/settings/passkeys/finish", func(ctx *web.Context) error {
		if err := ctx.FinishPasskey(strings.TrimSpace(ctx.QueryParam("name"))); err != nil {
			ctx.Logger().Printf("passkey refused: %v", err)
			return ctx.JSON(http.StatusUnprocessableEntity, map[string]string{"error": ctx.T("passkeys.addrefused")})
		}
		ctx.PutNotice(ctx.T("notice.passkeyadded"))
		return ctx.NoContent(http.StatusNoContent)
	})
	g.POST("passkeys.remove", "/settings/passkeys/remove", func(ctx *web.Context) error {
		s := ctx.Session()
		if !s.RemovePasskey(ctx.FormValue("passkey")) {
			return alborz.NotFound("notfound.passkey")
		}
		if err := ctx.Server.Sessions.Save(s); err != nil {
			return err
		}
		ctx.PutNotice(ctx.T("notice.passkeyremoved"))
		return ctx.Redirect(http.StatusFound, "/settings/passkeys")
	})
}
