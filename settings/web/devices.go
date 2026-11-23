package web

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

// DevicesRenderData renders devices.html.
type DevicesRenderData struct {
	web.PageData[map[string][]web.RailRow]
	Devices []DeviceRow
	Only    bool
	// Made is the device password just made, on the one page that shows it.
	Made *MadeDevice
}

// MadeDevice is what a client is set up with.
type MadeDevice struct {
	Address, Username, Password string
}

// DeviceRow is a device password and what it reaches in words.
type DeviceRow struct {
	alborz.DevicePassword
	Reaches []string
}

// DeviceRenderData renders device.html: a device password and the collections it is limited to.
type DeviceRenderData struct {
	web.PageData[map[string][]web.RailRow]
	Device alborz.DevicePassword
	Groups []DeviceGroup
}

// DeviceCreateRenderData renders device-create.html.
type DeviceCreateRenderData struct {
	web.PageData[map[string][]web.RailRow]
	Name     string
	ReadOnly bool
	// Scoped limits the password to the chosen rows of Groups (ADR 109).
	Scoped bool
	Groups []DeviceGroup
}

// DeviceGroup are the collections of one kind, under its name in the nav.
type DeviceGroup struct {
	Label string
	Rows  []ScopeRow
}

// ScopeRow is a collection a device password is or can be limited to.
type ScopeRow struct {
	alborz.DeviceChoice
	// Apart is the collection's ID, the part of its address to a DAV client that no other collection of its
	// owner has; set where another row reads the same without it.
	Apart string
	// Chosen is what the form holds for it.
	Chosen bool
	// Gone is a collection of a password's scope the account no longer sees, which has no name left.
	Gone bool
}

// deviceKinds are the kinds of collection in the order the nav has them, each with its name there and the
// plural that counts it.
var deviceKinds = []struct{ kind, label, count string }{
	{"calendar", "nav.calendar", "devices.calendars"},
	{"tasks", "nav.tasks", "devices.tasklists"},
	{"journal", "nav.journal", "devices.journals"},
	{"addressbook", "nav.contacts", "devices.addressbooks"},
}

// scopeRows are the collections the account sees now, which a device password can be limited to.
func scopeRows(ctx *web.Context) ([]ScopeRow, error) {
	if ctx.Server.DeviceChoices == nil {
		return nil, nil
	}
	choices, err := ctx.Server.DeviceChoices(ctx.Login.Username())
	rows := make([]ScopeRow, len(choices))
	for i, c := range choices {
		rows[i].DeviceChoice = c
		alike := func(o alborz.DeviceChoice) bool {
			return o.Kind == c.Kind && o.Name == c.Name && o.SharedBy == c.SharedBy && o.Subscribed == c.Subscribed && o.Path != c.Path
		}
		if slices.ContainsFunc(choices, alike) {
			rows[i].Apart = c.ID
		}
	}
	return rows, err
}

// held are the rows of a device password's scope; a collection that is not among those the account sees now
// is gone.
func held(p alborz.DevicePassword, rows []ScopeRow) []ScopeRow {
	out := make([]ScopeRow, len(p.Scope))
	for i, c := range p.Scope {
		at := slices.IndexFunc(rows, func(row ScopeRow) bool { return row.Ref == c.Ref })
		if at < 0 {
			out[i] = ScopeRow{DeviceCollection: c, Gone: true}
		} else {
			out[i] = rows[at]
		}
	}
	return out
}

// grouped sorts rows into their kinds.
func grouped(rows []ScopeRow) []DeviceGroup {
	var groups []DeviceGroup
	for _, k := range deviceKinds {
		group := DeviceGroup{Label: k.label}
		for _, row := range rows {
			if row.Kind == k.kind {
				group.Rows = append(group.Rows, row)
			}
		}
		if group.Rows != nil {
			groups = append(groups, group)
		}
	}
	return groups
}

// reaches says a device password's scope: every collection, or how many of each kind and how many no longer
// exist.
func reaches(ctx *web.Context, p alborz.DevicePassword, rows []ScopeRow) []string {
	if !p.Scoped {
		return []string{ctx.T("devices.scopeall")}
	}
	scope := held(p, rows)
	count := func(is func(ScopeRow) bool) int {
		n := 0
		for _, row := range scope {
			if is(row) {
				n++
			}
		}
		return n
	}
	var out []string
	for _, k := range deviceKinds {
		if n := count(func(row ScopeRow) bool { return row.Kind == k.kind && !row.Gone }); n > 0 {
			out = append(out, ctx.Tf(k.count, n))
		}
	}
	if n := count(func(row ScopeRow) bool { return row.Gone }); n > 0 {
		out = append(out, ctx.Tf("devices.gone", n))
	}
	return out
}

func renderDevices(ctx *web.Context, made *MadeDevice) error {
	devices, only, err := ctx.Server.Devices.List(ctx.Login.Username())
	if err != nil {
		return err
	}
	rows, err := scopeRows(ctx)
	if err != nil {
		return err
	}
	data := &DevicesRenderData{PageData: *web.NewSettingsPageData(ctx, ctx.T("devices.title")), Only: only, Made: made}
	for _, p := range devices {
		data.Devices = append(data.Devices, DeviceRow{p, reaches(ctx, p, rows)})
	}
	if made != nil {
		data.GlobalData.Notice = &web.Notice{Kind: web.NoticeDone, Text: ctx.T("notice.devicemade")}
	}
	return ctx.Render(http.StatusOK, "settings/devices", data)
}

func handleDevices(ctx *web.Context) error { return renderDevices(ctx, nil) }

func handleDevice(ctx *web.Context) error {
	devices, _, err := ctx.Server.Devices.List(ctx.Login.Username())
	if err != nil {
		return err
	}
	at := slices.IndexFunc(devices, func(p alborz.DevicePassword) bool { return p.ID == ctx.Param("device") })
	if at < 0 {
		return alborz.NotFound("notfound.device")
	}
	rows, err := scopeRows(ctx)
	if err != nil {
		return err
	}
	p := devices[at]
	return ctx.Render(http.StatusOK, "settings/device", &DeviceRenderData{
		PageData: *web.NewSettingsPageData(ctx, p.Name), Device: p, Groups: grouped(held(p, rows)),
	})
}

func handleDeviceCreate(ctx *web.Context) error {
	rows, err := scopeRows(ctx)
	if err != nil {
		return err
	}
	data := &DeviceCreateRenderData{PageData: *web.NewSettingsPageData(ctx, ctx.T("devices.new")), Groups: grouped(rows)}
	if ctx.Request().Method != http.MethodPost {
		return ctx.Render(http.StatusOK, "settings/device-create", data)
	}
	form, err := ctx.FormParams()
	if err != nil {
		return err
	}
	data.Name, data.ReadOnly = strings.TrimSpace(ctx.FormValue("name")), ctx.FormValue("access") == "read"
	data.Scoped = ctx.FormValue("scope") == "some"
	var scope []alborz.DeviceCollection
	for i := range rows {
		if rows[i].Chosen = slices.Contains(form["collection"], rows[i].Path); rows[i].Chosen {
			scope = append(scope, rows[i].DeviceCollection)
		}
	}
	data.Groups = grouped(rows)
	switch {
	case data.Name == "":
		return web.RenderRefused(ctx, "settings/device-create", data, ctx.T("devices.noname"))
	case data.Scoped && len(form["collection"]) == 0:
		return web.RenderRefused(ctx, "settings/device-create", data, ctx.T("devices.nocollection"))
	case data.Scoped && len(scope) != len(form["collection"]):
		return web.RenderRefused(ctx, "settings/device-create", data, ctx.T("notfound.collection"))
	}
	var password string
	if data.Scoped {
		password, err = ctx.Server.CreateScopedDevicePassword(ctx.Login, data.Name, data.ReadOnly, scope)
	} else {
		password, err = ctx.Server.CreateDevicePassword(ctx.Login, data.Name, data.ReadOnly)
	}
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
