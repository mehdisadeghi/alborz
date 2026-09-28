package alborzbase

import (
	"net/http"
	"strings"
	"time"

	"git.mehdix.org/alborz"
)

// PasskeysRenderData renders passkeys.html.
type PasskeysRenderData struct {
	alborz.BaseRenderData
	Rail     map[string][]alborz.RailRow
	Passkeys []alborz.Passkey
}

// PasskeyRenderData renders passkey-create.html.
type PasskeyRenderData struct {
	alborz.BaseRenderData
	Rail map[string][]alborz.RailRow
}

// UnlockRenderData renders unlock.html.
type UnlockRenderData struct {
	alborz.BaseRenderData
	Next string
}

func registerPasskeyRoutes(p *alborz.GoPlugin) {
	p.GET("/settings/passkeys", func(ctx *alborz.Context) error {
		return ctx.Render(http.StatusOK, "passkeys.html", &PasskeysRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("passkeys.title")),
			Rail:           settingsRail(ctx),
			Passkeys:       ctx.Session().Lock().Passkeys,
		})
	})
	passkeyForm := func(ctx *alborz.Context) error {
		data := &PasskeyRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("passkeys.new")),
			Rail:           settingsRail(ctx),
		}
		// Only a form without script posts here, and without script there is no passkey prompt.
		if ctx.Request().Method == http.MethodPost {
			data.Refused(ctx.T("passkeys.needsscript"))
			return ctx.Render(http.StatusUnprocessableEntity, "passkey-create.html", data)
		}
		return ctx.Render(http.StatusOK, "passkey-create.html", data)
	}
	p.GET("/settings/passkeys/create", passkeyForm)
	p.POST("/settings/passkeys/create", passkeyForm)
	p.POST("/settings/passkeys/begin", func(ctx *alborz.Context) error {
		options, err := ctx.BeginPasskey()
		if err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, options)
	})
	p.POST("/settings/passkeys/finish", func(ctx *alborz.Context) error {
		if err := ctx.FinishPasskey(strings.TrimSpace(ctx.QueryParam("name"))); err != nil {
			ctx.Logger().Printf("passkey refused: %v", err)
			return ctx.JSON(http.StatusUnprocessableEntity, map[string]string{"error": ctx.T("passkeys.addrefused")})
		}
		ctx.PutNotice(ctx.T("notice.passkeyadded"))
		return ctx.NoContent(http.StatusNoContent)
	})
	p.POST("/settings/passkeys/remove", func(ctx *alborz.Context) error {
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

	p.POST("/lock", func(ctx *alborz.Context) error {
		ctx.Session().LockNow()
		// An empty change wakes this session's other tabs to find the lock.
		ctx.Server.Changes.Publish(alborz.Change{})
		ctx.Quiet()
		return ctx.Redirect(http.StatusSeeOther, "/unlock")
	})
	// Any request counts as presence, so this one needs to do nothing.
	p.POST("/alive", func(ctx *alborz.Context) error {
		return ctx.NoContent(http.StatusNoContent)
	})

	p.GET("/unlock", func(ctx *alborz.Context) error {
		next := ctx.NextOr("/")
		if !ctx.Session().Locked(time.Now()) {
			return ctx.Redirect(http.StatusFound, next)
		}
		return ctx.Render(http.StatusOK, "unlock.html", &UnlockRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("lock.title")),
			Next:           next,
		})
	})
	p.POST("/unlock/begin", func(ctx *alborz.Context) error {
		options, err := ctx.BeginUnlock()
		if err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, options)
	})
	p.POST("/unlock/finish", func(ctx *alborz.Context) error {
		if err := ctx.FinishUnlock(); err != nil {
			ctx.Logger().Printf("unlock refused: %v", err)
			return ctx.JSON(http.StatusForbidden, map[string]string{"error": ctx.T("passkeys.refused")})
		}
		return ctx.NoContent(http.StatusNoContent)
	})
	p.POST("/unlock/leave", func(ctx *alborz.Context) error {
		ctx.EndSession()
		return ctx.Redirect(http.StatusFound, "/login")
	})
}
