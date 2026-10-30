package web

import (
	"fmt"
	"net/http"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/webpush"
	"git.mehdix.org/alborz/web"
)

// NotificationsRenderData renders notifications.html.
type NotificationsRenderData struct {
	web.PageData[map[string][]web.RailRow]
	Accounts []AccountNotifications
	Key      string
	// Days and Minutes are how long a login is kept for notifications, remembered and not.
	Days, Minutes int
}

// AccountNotifications is one account's state for this browser and its other devices.
type AccountNotifications struct {
	Account string
	Current *NotifiedDevice
	Other   []NotifiedDevice
}

// NotifiedDevice is a device as the list shows it.
type NotifiedDevice struct {
	ID, Device string
	Created    time.Time
	// Current is this browser; Gone a browser signed out of the account, which is sent nothing.
	Current, Gone bool
}

func notificationData(ctx *web.Context) (*NotificationsRenderData, error) {
	session := ctx.Session().ID
	data := &NotificationsRenderData{
		PageData: *web.NewSettingsPageData(ctx, ctx.T("notifications.title")),
		Key:      ctx.Server.Pushes.Key.Public(),
		Days:     int(alborz.CredentialCookieLife() / (24 * time.Hour)),
		Minutes:  int(ctx.Server.Options.LoginIdle / time.Minute),
	}
	for _, login := range ctx.Logins() {
		account := login.Username()
		devices, err := ctx.Server.Pushes.Devices(account)
		if err != nil {
			return nil, err
		}
		row := AccountNotifications{Account: account}
		for _, d := range devices {
			shown := NotifiedDevice{ID: ctx.Server.Pushes.ID(d), Device: d.Device, Created: d.Created,
				Current: d.Session == session, Gone: !ctx.Server.Sessions.Holds(d.Session, account)}
			if shown.Current {
				if row.Current == nil {
					row.Current = &shown
				}
			} else {
				row.Other = append(row.Other, shown)
			}
		}
		data.Accounts = append(data.Accounts, row)
	}
	return data, nil
}

func handleNotifications(ctx *web.Context) error {
	data, err := notificationData(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "settings/notifications", data)
}

// The form names an account explicitly; a browser subscription may be shared by several of them.
func notificationAccount(ctx *web.Context) (*alborz.Login, error) {
	account := ctx.FormValue("account")
	if account == "" && len(ctx.Logins()) == 1 {
		return ctx.Login, nil
	}
	if login := ctx.LoginFor(account); login != nil {
		return login, nil
	}
	return nil, web.NewError(http.StatusBadRequest, fmt.Errorf(ctx.T("account.notsignedin"), account))
}

// handleNotificationCreate keeps the shared subscription for the account the reader turned on.
func handleNotificationCreate(ctx *web.Context) error {
	data, err := notificationData(ctx)
	if err != nil {
		return err
	}
	if ctx.FormValue("endpoint") == "" {
		return web.RenderRefused(ctx, "settings/notifications", data, ctx.T("notifications.needsscript"))
	}
	sub, err := webpush.Parse(ctx.FormValue("endpoint"), ctx.FormValue("p256dh"), ctx.FormValue("auth"))
	if err != nil {
		ctx.Logger().Printf("push subscription refused: %v", err)
		return web.RenderRefused(ctx, "settings/notifications", data, ctx.T("notifications.refused"))
	}
	// The worker posts a subscription the push service replaced, for each account of this browser that had it.
	if old := ctx.FormValue("old"); old != "" {
		for _, login := range ctx.Logins() {
			if err := ctx.Server.Pushes.Renew(login.Username(), old, ctx.Session().ID, sub); err != nil {
				return err
			}
		}
		return ctx.QuietRedirect(ctx.Path("settings.notifications"))
	}
	login, err := notificationAccount(ctx)
	if err != nil {
		return err
	}
	err = ctx.Server.Pushes.Put(login.Username(), alborz.PushDevice{
		Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth,
		Session: ctx.Session().ID, Device: ctx.Request().UserAgent(), Lang: ctx.PageLanguage(),
		Zone: web.UserLocation(ctx).String(), Origin: ctx.Origin(), Created: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	return ctx.NoticeRedirect(ctx.T("notice.notificationson"), ctx.Path("settings.notifications"))
}

func handleNotificationOff(ctx *web.Context) error {
	login, err := notificationAccount(ctx)
	if err != nil {
		return err
	}
	account, id := login.Username(), ctx.FormValue("device")
	devices, err := ctx.Server.Pushes.Devices(account)
	if err != nil {
		return err
	}
	// A rotated subscription can leave older endpoints for this browser. Turning it off ends all of them.
	current := false
	for _, d := range devices {
		if ctx.Server.Pushes.ID(d) == id && d.Session == ctx.Session().ID {
			current = true
			break
		}
	}
	if current {
		err = ctx.Server.Pushes.SignedOut(account, ctx.Session().ID)
	} else {
		err = ctx.Server.Pushes.Remove(account, id)
	}
	if err != nil {
		return err
	}
	ctx.Server.Recheck(login.Username())
	return ctx.NoticeRedirect(ctx.T("notice.notificationsoff"), ctx.Path("settings.notifications"))
}
