package web

import (
	"errors"
	"net/http"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

// DevicesRenderData renders devices.html.
type DevicesRenderData struct {
	web.PageData[map[string][]web.RailRow]
	Devices []alborz.DevicePassword
	Only    bool
	// Made is the device password just made, on the one page that shows it.
	Made *MadeDevice
}

// MadeDevice is what a client is set up with.
type MadeDevice struct {
	Address, Username, Password string
}

// DeviceRenderData renders device-create.html.
type DeviceRenderData struct {
	web.PageData[map[string][]web.RailRow]
	Name     string
	ReadOnly bool
}

func renderDevices(ctx *web.Context, made *MadeDevice) error {
	devices, only, err := ctx.Server.Devices.List(ctx.Login.Username())
	if err != nil {
		return err
	}
	data := &DevicesRenderData{PageData: *web.NewSettingsPageData(ctx, ctx.T("devices.title")), Devices: devices, Only: only, Made: made}
	if made != nil {
		data.GlobalData.Notice = &web.Notice{Kind: web.NoticeDone, Text: ctx.T("notice.devicemade")}
	}
	return ctx.Render(http.StatusOK, "settings/devices", data)
}

func handleDevices(ctx *web.Context) error { return renderDevices(ctx, nil) }

func handleDeviceCreate(ctx *web.Context) error {
	data := &DeviceRenderData{PageData: *web.NewSettingsPageData(ctx, ctx.T("devices.new"))}
	if ctx.Request().Method != http.MethodPost {
		return ctx.Render(http.StatusOK, "settings/device-create", data)
	}
	data.Name, data.ReadOnly = strings.TrimSpace(ctx.FormValue("name")), ctx.FormValue("access") == "read"
	if data.Name == "" {
		return web.RenderRefused(ctx, "settings/device-create", data, ctx.T("devices.noname"))
	}
	password, err := ctx.Server.CreateDevicePassword(ctx.Login, data.Name, data.ReadOnly)
	if errors.Is(err, alborz.ErrLocked) {
		return web.RenderRefused(ctx, "settings/device-create", data, ctx.SaveRefusal(err))
	}
	if err != nil {
		return err
	}
	return renderDevices(ctx, &MadeDevice{
		Address:  ctx.Origin() + ctx.Path("dav.collections"),
		Username: ctx.Login.Username(),
		Password: password,
	})
}

func handleDeviceRevoke(ctx *web.Context) error {
	found, err := ctx.Server.Devices.Revoke(ctx.Login.Username(), ctx.FormValue("device"))
	if err != nil {
		return err
	}
	if !found {
		return alborz.NotFound("notfound.device")
	}
	return ctx.NoticeRedirect(ctx.T("notice.devicerevoked"), ctx.URL("settings.devices"))
}

func handleDevicesOnly(ctx *web.Context) error {
	if err := ctx.Server.Devices.SetOnly(ctx.Login.Username(), ctx.FormValue("only") == "1"); err != nil {
		return err
	}
	return ctx.NoticeRedirect(ctx.T("notice.settingssaved"), ctx.URL("settings.devices"))
}
