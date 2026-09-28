package mail

import (
	"net/http"
	"time"

	"git.mehdix.org/alborz"
)

// UnlockRenderData renders unlock.html.
type UnlockRenderData struct {
	alborz.BaseRenderData
	Next string
}

func registerPasskeyRoutes(p alborz.Router) {
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
		return ctx.Render(http.StatusOK, "unlock", &UnlockRenderData{
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
