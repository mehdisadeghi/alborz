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
	web.BaseRenderData
	Rail    map[string][]web.RailRow
	Devices []NotifiedDevice
	Minutes int
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
	web.BaseRenderData
	Rail    map[string][]web.RailRow
	Key     string
	Minutes int
}

// loginMinutes is how long notifications outlive the account's last use: its login's life.
const loginMinutes = int(alborz.LoginDuration / time.Minute)

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
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("notifications.title")),
		Rail:           web.SettingsRail(ctx),
		Devices:        shown,
		Minutes:        loginMinutes,
	})
}

// handleNotificationCreate keeps the subscription the page's script made; without a script the form
// carries none, and is refused.
func handleNotificationCreate(ctx *web.Context) error {
	data := &NotificationRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("notifications.new")),
		Rail:           web.SettingsRail(ctx),
		Key:            ctx.Server.Pushes.Key.Public(),
		Minutes:        loginMinutes,
	}
	if ctx.Request().Method != http.MethodPost {
		return ctx.Render(http.StatusOK, "settings/notification-create", data)
	}
	if ctx.FormValue("endpoint") == "" {
		data.Refused(ctx.T("notifications.needsscript"))
		return ctx.Render(http.StatusUnprocessableEntity, "settings/notification-create", data)
	}
	sub, err := webpush.Parse(ctx.FormValue("endpoint"), ctx.FormValue("p256dh"), ctx.FormValue("auth"))
	if err != nil {
		ctx.Logger().Printf("push subscription refused: %v", err)
		data.Refused(ctx.T("notifications.refused"))
		return ctx.Render(http.StatusUnprocessableEntity, "settings/notification-create", data)
	}
	// The worker posts a subscription the push service replaced, for each account of this browser that had it.
	if old := ctx.FormValue("old"); old != "" {
		for _, login := range ctx.Logins() {
			if err := ctx.Server.Pushes.Renew(login.Username(), old, ctx.Session().ID, sub); err != nil {
				return err
			}
		}
		ctx.Quiet()
		return ctx.Redirect(http.StatusFound, ctx.URL("settings.notifications"))
	}
	err = ctx.Server.Pushes.Put(ctx.Login.Username(), alborz.PushDevice{
		Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth,
		Session: ctx.Session().ID, Device: ctx.Request().UserAgent(), Lang: ctx.PageLanguage(),
		Zone: web.UserLocation(ctx).String(), Origin: ctx.Origin(), Created: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	ctx.PutNotice(ctx.T("notice.notificationson"))
	return ctx.Redirect(http.StatusFound, ctx.URL("settings.notifications"))
}

func handleNotificationOff(ctx *web.Context) error {
	if err := ctx.Server.Pushes.Remove(ctx.Login.Username(), ctx.FormValue("device")); err != nil {
		return err
	}
	ctx.PutNotice(ctx.T("notice.notificationsoff"))
	return ctx.Redirect(http.StatusFound, ctx.URL("settings.notifications"))
}
