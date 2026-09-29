package auth

import (
	"net/http"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

// UnlockRenderData renders unlock.html.
type UnlockRenderData struct {
	web.BaseRenderData
	Next string
}

func registerPasskeyRoutes(unlock, app *web.Group) {
	app.POST("lock", "/lock", func(ctx *web.Context) error {
		ctx.Session().LockNow()
		// An empty change wakes this session's other tabs to find the lock.
		ctx.Server.Changes.Publish(alborz.Change{})
		ctx.Quiet()
		return ctx.Redirect(http.StatusSeeOther, ctx.Path("auth.unlock"))
	})
	// Any request counts as presence, so this one needs to do nothing.
	app.POST("alive", "/alive", func(ctx *web.Context) error {
		return ctx.NoContent(http.StatusNoContent)
	})

	unlock.GET("unlock", "", func(ctx *web.Context) error {
		next := ctx.NextOr(ctx.Path("mail.home"))
		if !ctx.Session().Locked(time.Now()) {
			return ctx.Redirect(http.StatusFound, next)
		}
		return ctx.Render(http.StatusOK, "unlock", &UnlockRenderData{
			BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("lock.title")),
			Next:           next,
		})
	})
	unlock.POST("unlock.begin", "/begin", func(ctx *web.Context) error {
		options, err := ctx.BeginUnlock()
		if err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, options)
	})
	unlock.POST("unlock.finish", "/finish", func(ctx *web.Context) error {
		if err := ctx.FinishUnlock(); err != nil {
			ctx.Logger().Printf("unlock refused: %v", err)
			return ctx.JSON(http.StatusForbidden, map[string]string{"error": ctx.T("passkeys.refused")})
		}
		return ctx.NoContent(http.StatusNoContent)
	})
	unlock.POST("unlock.leave", "/leave", func(ctx *web.Context) error {
		ctx.EndSession()
		return ctx.Redirect(http.StatusFound, ctx.Path("auth.login"))
	})
}
