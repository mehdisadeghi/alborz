package web

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
)

var serviceRefusals = map[error]string{
	alborz.ErrServiceNotURL:   "settings.servernoturl",
	alborz.ErrServiceNotHTTPS: "settings.servernothttps",
	alborz.ErrServiceNoHost:   "settings.servernohost",
	alborz.ErrServicePrivate:  "settings.serverprivate",
}

var placeIDs = []string{"domain", "host", "own", "here"}

// ServerRenderData is the DAV servers' page, with the servers the account names for itself.
type ServerRenderData struct {
	web.BaseRenderData
	Rail map[string][]web.RailRow
	*web.Servers
	Services        dav.Services
	HasHTTPPassword bool
}

// DefaultPlace is the account's default place, else the first offered.
func (d *ServerRenderData) DefaultPlace() string {
	for _, p := range d.Places {
		if p.ID == d.Services.Default {
			return p.ID
		}
	}
	if len(d.Places) > 0 {
		return d.Places[0].ID
	}
	return ""
}

func handleServer(ctx *web.Context) error {
	data := &ServerRenderData{
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("servers." + web.ServerDAV)),
		Rail:           web.SettingsRail(ctx),
	}
	render := func(status int) error {
		servers, err := ctx.Servers(web.ServerDAV)
		if err != nil {
			return err
		}
		data.Servers = servers
		return ctx.Render(status, "settings/server", data)
	}
	var err error
	if data.HasHTTPPassword, err = ctx.Login.HasHTTPPassword(ctx.Request().Context()); err != nil {
		return err
	}
	if data.Services, err = dav.LoadServices(ctx.Request().Context(), ctx.Login.Store()); err != nil {
		return err
	}
	if ctx.Request().Method != http.MethodPost {
		return render(http.StatusOK)
	}

	// What was typed is what the form shows again when one is refused.
	named := dav.Services{
		CalDAV:   strings.TrimSpace(ctx.FormValue("caldav_url")),
		CardDAV:  strings.TrimSpace(ctx.FormValue("carddav_url")),
		Username: strings.TrimSpace(ctx.FormValue("dav_username")),
	}
	if d := ctx.FormValue("dav_default"); slices.Contains(placeIDs, d) {
		named.Default = d
	}
	for _, server := range []string{named.CalDAV, named.CardDAV} {
		if server == "" {
			continue
		}
		if err := ctx.Server.CheckServiceURL(ctx.Request().Context(), server); err != nil {
			data.Services = named
			data.Refused(fmt.Sprintf(ctx.T(serviceRefusals[err]), server))
			return render(http.StatusUnprocessableEntity)
		}
	}
	// An empty field leaves the stored password alone; only the reset box lets it go.
	refuse := func(err error) error {
		data.Services = named
		data.Refused(ctx.SaveRefusal(err))
		return render(http.StatusUnprocessableEntity)
	}
	if ctx.FormValue("http_password_reset") != "" {
		err = ctx.Login.SetHTTPPassword(ctx.Request().Context(), "")
	} else if p := ctx.FormValue("http_password"); p != "" {
		err = ctx.Login.SetHTTPPassword(ctx.Request().Context(), p)
	}
	if err != nil {
		return refuse(err)
	}
	if named != data.Services {
		if err := ctx.Login.Store().Put(ctx.Request().Context(), dav.ServicesKey, &named); err != nil {
			return refuse(err)
		}
	}
	// The cache was read with the servers or password just left.
	ctx.Server.ForgetAccount(ctx.Login.Username())
	ctx.PutNotice(ctx.T("notice.serverssaved"))
	return ctx.Redirect(http.StatusFound, ctx.URL("pim.server"))
}
