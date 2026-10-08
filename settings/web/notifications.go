package web

import (
	"net/http"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/webpush"
	"git.mehdix.org/alborz/web"
)

// NotificationsRenderData renders notifications.html.
type NotificationsRenderData struct {
	web.PageData[map[string][]web.RailRow]
	Devices []NotifiedDevice
	// Days and Minutes are how long a login is kept for notifications, remembered and not.
	Days, Minutes int
}

// NotifiedDevice is a device as the list shows it.
type NotifiedDevice struct {
	ID, Device string
	Created    time.Time
	// Current is this browser; Gone a browser signed out of the account, which is sent nothing.
	Current, Gone bool
}

// NotificationRenderData renders notification-create.html.
type NotificationRenderData struct {
	web.PageData[map[string][]web.RailRow]
	Key           string
	Days, Minutes int
}

func handleNotifications(ctx *web.Context) error {
	account := ctx.Login.Username()
	devices, err := ctx.Server.Pushes.Devices(account)
	if err != nil {
		return err
	}
	session := ctx.Session().ID
	var shown []NotifiedDevice
	for _, d := range devices {
		shown = append(shown, NotifiedDevice{ID: ctx.Server.Pushes.ID(d), Device: d.Device, Created: d.Created,
			Current: d.Session == session, Gone: !ctx.Server.Sessions.Holds(d.Session, account)})
	}
	return ctx.Render(http.StatusOK, "settings/notifications", &NotificationsRenderData{
		PageData: *web.NewSettingsPageData(ctx, ctx.T("notifications.title")),
		Devices:  shown,
		Days:     int(alborz.CredentialCookieLife() / (24 * time.Hour)),
		Minutes:  int(ctx.Server.Options.LoginIdle / time.Minute),
	})
}

// handleNotificationCreate keeps the subscription the page's script made; without a script the form
// carries none, and is refused.
func handleNotificationCreate(ctx *web.Context) error {
	data := &NotificationRenderData{
		PageData: *web.NewSettingsPageData(ctx, ctx.T("notifications.new")),
		Key:      ctx.Server.Pushes.Key.Public(),
		Days:     int(alborz.CredentialCookieLife() / (24 * time.Hour)),
		Minutes:  int(ctx.Server.Options.LoginIdle / time.Minute),
	}
	if ctx.Request().Method != http.MethodPost {
		return ctx.Render(http.StatusOK, "settings/notification-create", data)
	}
	if ctx.FormValue("endpoint") == "" {
		return web.RenderRefused(ctx, "settings/notification-create", data, ctx.T("notifications.needsscript"))
	}
	sub, err := webpush.Parse(ctx.FormValue("endpoint"), ctx.FormValue("p256dh"), ctx.FormValue("auth"))
	if err != nil {
		ctx.Logger().Printf("push subscription refused: %v", err)
		return web.RenderRefused(ctx, "settings/notification-create", data, ctx.T("notifications.refused"))
	}
	// The worker posts a subscription the push service replaced, for each account of this browser that had it.
	if old := ctx.FormValue("old"); old != "" {
		for _, login := range ctx.Logins() {
			if err := ctx.Server.Pushes.Renew(login.Username(), old, ctx.Session().ID, sub); err != nil {
				return err
			}
		}
		return ctx.QuietRedirect(ctx.URL("settings.notifications"))
	}
	err = ctx.Server.Pushes.Put(ctx.Login.Username(), alborz.PushDevice{
		Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth,
		Session: ctx.Session().ID, Device: ctx.Request().UserAgent(), Lang: ctx.PageLanguage(),
		Zone: web.UserLocation(ctx).String(), Origin: ctx.Origin(), Created: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	return ctx.NoticeRedirect(ctx.T("notice.notificationson"), ctx.URL("settings.notifications"))
}

func handleNotificationOff(ctx *web.Context) error {
	if err := ctx.Server.Pushes.Remove(ctx.Login.Username(), ctx.FormValue("device")); err != nil {
		return err
	}
	ctx.Server.Recheck(ctx.Login.Username())
	return ctx.NoticeRedirect(ctx.T("notice.notificationsoff"), ctx.URL("settings.notifications"))
}
