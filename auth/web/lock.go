package web

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
		if len(ctx.Session().Lock().Passkeys) == 0 {
			ctx.Quiet()
			return ctx.Redirect(http.StatusSeeOther, ctx.Path("settings.passkeys.create"))
		}
		ctx.Session().LockNow()
		// An empty change wakes this session's other tabs to find the lock.
		ctx.Server.Changes.Publish(alborz.Change{})
		ctx.Quiet()
		if ctx.Request().Header.Get("Alborz-Lock") == "1" {
			return ctx.NoContent(http.StatusNoContent)
		}
		return ctx.Redirect(http.StatusSeeOther, ctx.Path("auth.unlock"))
	})
	unlock.GET("unlock.state", "/state", func(ctx *web.Context) error {
		s := ctx.Session()
		lock := s.Lock()
		ctx.Response().Header().Set("Cache-Control", "no-store")
		return ctx.JSON(http.StatusOK, map[string]any{
			"session": s.ID, "protected": len(lock.Passkeys) > 0,
			"locked": s.Locked(time.Now()), "after": int(lock.Wait() / time.Second),
		})
	})
	// Any request counts as presence, so this one needs to do nothing.
	app.POST("alive", "/alive", func(ctx *web.Context) error {
		return ctx.NoContent(http.StatusNoContent)
	})

	unlock.GET("unlock", "", func(ctx *web.Context) error {
		next := ctx.NextOr(ctx.Path("mail.home"))
		if !ctx.Session().Locked(time.Now()) {
			return ctx.FoundRedirect(next)
		}
		return ctx.Render(http.StatusOK, "unlock", &UnlockRenderData{
			BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("lock.title")),
			Next:           next,
		})
	})
	unlock.POST("unlock.begin", "/begin", func(ctx *web.Context) error {
		return web.PasskeyBegin(ctx, ctx.BeginUnlock)
	})
	unlock.POST("unlock.finish", "/finish", func(ctx *web.Context) error {
		if err := ctx.FinishUnlock(); err != nil {
			return web.PasskeyFailure(ctx, http.StatusForbidden, "unlock refused", ctx.T("passkeys.refused"), err)
		}
		return ctx.NoContent(http.StatusNoContent)
	})
	unlock.POST("unlock.leave", "/leave", func(ctx *web.Context) error {
		ctx.EndSession()
		if ctx.Partial() {
			return ctx.Relocate(ctx.Path("auth.login"))
		}
		return ctx.FoundRedirect(ctx.Path("auth.login"))
	})
}
