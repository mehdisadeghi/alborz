package alborzbase

import (
	"net/http"
	"strings"
	"time"

	"git.mehdix.org/alborz"
)

// PasskeysRenderData renders passkeys.html: the passkeys that unlock
// this browser, each with the way to take it back.
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
			Passkeys:       ctx.Visit().Lock().Passkeys,
		})
	})
	passkeyForm := func(ctx *alborz.Context) error {
		data := &PasskeyRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("passkeys.new")),
			Rail:           settingsRail(ctx),
		}
		// The form posts only where no script took it over, and without
		// one there is no prompt to make a passkey with.
		if ctx.Request().Method == http.MethodPost {
			data.Refused(ctx.T("passkeys.needsscript"))
			return ctx.Render(http.StatusUnprocessableEntity, "passkey-create.html", data)
		}
		return ctx.Render(http.StatusOK, "passkey-create.html", data)
	}
	p.GET("/settings/passkeys/create", passkeyForm)
	p.POST("/settings/passkeys/create", passkeyForm)
	// Registering is two posts with the authenticator between them; the
	// script on the form makes them, and says on the form what failed.
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
		v := ctx.Visit()
		if !v.RemovePasskey(ctx.FormValue("passkey")) {
			return alborz.NotFound("notfound.passkey")
		}
		if err := ctx.Server.Visits.Save(v); err != nil {
			return err
		}
		ctx.PutNotice(ctx.T("notice.passkeyremoved"))
		return ctx.Redirect(http.StatusFound, "/settings/passkeys")
	})

	p.POST("/lock", func(ctx *alborz.Context) error {
		ctx.Visit().LockNow()
		// A change that is nobody's tells no page anything, and wakes
		// this visit's streams in its other tabs to find the lock.
		ctx.Server.Changes.Publish(alborz.Change{})
		return ctx.Redirect(http.StatusSeeOther, "/unlock")
	})
	// The reader is at the keyboard with nothing to ask for: the request
	// is the whole of what it does, since any request counts as being here.
	p.POST("/alive", func(ctx *alborz.Context) error {
		return ctx.NoContent(http.StatusNoContent)
	})

	p.GET("/unlock", func(ctx *alborz.Context) error {
		next := ctx.NextOr("/")
		// Not locked, nothing to do here.
		if !ctx.Visit().Locked(time.Now()) {
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
		ctx.EndVisit()
		return ctx.Redirect(http.StatusFound, "/login")
	})
}
