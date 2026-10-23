package web

import (
	"errors"
	"net/http"
	"strconv"

	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
)

func notificationValidity(ctx *web.Context) (uint32, error) {
	validity, err := strconv.ParseUint(ctx.QueryParam("validity"), 10, 32)
	if err != nil || validity == 0 {
		return 0, web.NewError(http.StatusBadRequest, "invalid mailbox validity")
	}
	return uint32(validity), nil
}

// Both a normal click and recovery from a refused action validate the UID namespace before opening
// any message. A stale notification returns to the folder without marking a replacement UID read.
func handleNotificationOpen(ctx *web.Context) error {
	mailbox, uid, err := MessageRef(ctx)
	if err != nil {
		return err
	}
	validity, err := notificationValidity(ctx)
	if err != nil {
		return err
	}
	if err := mail.CheckNotification(ctx.Request().Context(), ctx.Login, mailbox, uid, validity); err != nil {
		if errors.Is(err, mail.ErrNotificationStale) {
			return ctx.FoundRedirect(ctx.URL("mail.mailbox", mailbox))
		}
		return err
	}
	return ctx.FoundRedirect(ctx.URL("mail.message", mailbox, uid))
}

// The app's normal session, passkey and SameOrigin middleware protect this endpoint. Requiring a
// non-simple header also prevents cross-origin HTML forms from triggering a notification action.
func (p *plugin) handleNotificationAction(ctx *web.Context) error {
	if ctx.Request().Header.Get("Alborz-Notification") != "1" {
		return web.NewError(http.StatusForbidden)
	}
	mailbox, uid, err := MessageRef(ctx)
	if err != nil {
		return err
	}
	validity, err := notificationValidity(ctx)
	if err != nil {
		return err
	}
	action := ctx.FormValue("action")
	if action != "read" && action != "delete" {
		return web.NewError(http.StatusBadRequest, "invalid notification action")
	}
	if err := p.NotificationAction(ctx.Request().Context(), ctx.Login, mailbox, uid, validity, action); err != nil {
		if errors.Is(err, mail.ErrNotificationStale) {
			return web.NewError(http.StatusConflict, err)
		}
		return err
	}
	return ctx.NoContent(http.StatusNoContent)
}
