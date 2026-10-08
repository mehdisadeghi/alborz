package web

import (
	"net/http"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

// PasskeysRenderData renders passkeys.html.
type PasskeysRenderData struct {
	web.PageData[map[string][]web.RailRow]
	Passkeys []alborz.Passkey
}

func registerPasskeyRoutes(g *web.Group) {
	g.GET("passkeys", "/settings/passkeys", func(ctx *web.Context) error {
		return ctx.Render(http.StatusOK, "settings/passkeys", &PasskeysRenderData{
			PageData: *web.NewSettingsPageData(ctx, ctx.T("passkeys.title")),
			Passkeys: ctx.Session().Lock().Passkeys,
		})
	})
	passkeyForm := func(ctx *web.Context) error {
		data := web.NewSettingsPageData(ctx, ctx.T("passkeys.new"))
		// Only a form without script posts here, and without script there is no passkey prompt.
		if ctx.Request().Method == http.MethodPost {
			return web.RenderRefused(ctx, "settings/passkey-create", data, ctx.T("passkeys.needsscript"))
		}
		return ctx.Render(http.StatusOK, "settings/passkey-create", data)
	}
	g.GET("passkeys.create", "/settings/passkeys/create", passkeyForm)
	g.POST("passkeys.create", "/settings/passkeys/create", passkeyForm)
	g.POST("passkeys.begin", "/settings/passkeys/begin", func(ctx *web.Context) error {
		return web.PasskeyBegin(ctx, ctx.BeginPasskey)
	})
	g.POST("passkeys.finish", "/settings/passkeys/finish", func(ctx *web.Context) error {
		if err := ctx.FinishPasskey(strings.TrimSpace(ctx.QueryParam("name"))); err != nil {
			return web.PasskeyFailure(ctx, http.StatusUnprocessableEntity, "passkey refused", ctx.T("passkeys.addrefused"), err)
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
		return ctx.NoticeRedirect(ctx.T("notice.passkeyremoved"), ctx.Path("settings.passkeys"))
	})
}
