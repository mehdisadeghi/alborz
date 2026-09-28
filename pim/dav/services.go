package dav

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
)

// Services are the calendar and contacts servers an account names for itself.
type Services struct {
	// CalDAV and CardDAV are the servers' URLs; empty is the domain's.
	CalDAV, CardDAV string
	// Username is what the servers know the account as; empty is the address.
	Username string
	// Default is where a new calendar or address book goes (ADR 28); empty is the first.
	Default string `json:",omitempty"`
}

const servicesKey = "services"

// LoadServices returns the account's own servers.
func LoadServices(ctx context.Context, s alborz.Store) (Services, error) {
	var v Services
	if err := s.Get(ctx, servicesKey, &v); err != nil && err != alborz.ErrNoStoreEntry {
		return Services{}, err
	}
	return v, nil
}

// setHTTPBasicAuth sets the login's credentials for the servers it names on req.
func setHTTPBasicAuth(login *alborz.Login, req *http.Request) error {
	password, err := login.HTTPPassword(req.Context())
	if err != nil {
		return err
	}
	services, err := LoadServices(req.Context(), login.Store())
	if err != nil {
		return err
	}
	username := login.Username()
	if services.Username != "" {
		username = services.Username
	}
	req.SetBasicAuth(username, password)
	return nil
}

var serviceRefusals = map[error]string{
	alborz.ErrServiceNotURL:   "settings.servernoturl",
	alborz.ErrServiceNotHTTPS: "settings.servernothttps",
	alborz.ErrServiceNoHost:   "settings.servernohost",
	alborz.ErrServicePrivate:  "settings.serverprivate",
}

var placeIDs = []string{"domain", "host", "own", "here"}

// ServerRenderData is the DAV servers' page, with the servers the account names for itself.
type ServerRenderData struct {
	*mail.ServersRenderData
	Services        Services
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

func handleServer(ctx *alborz.Context) error {
	servers, err := mail.NewServersRenderData(ctx, mail.ServerDAV)
	if err != nil {
		return err
	}
	data := &ServerRenderData{ServersRenderData: servers}
	render := func(status int) error {
		return mail.RenderServers(ctx, status, "server.html", data, servers)
	}
	if data.HasHTTPPassword, err = ctx.Login.HasHTTPPassword(ctx.Request().Context()); err != nil {
		return err
	}
	if data.Services, err = LoadServices(ctx.Request().Context(), ctx.Login.Store()); err != nil {
		return err
	}
	if ctx.Request().Method != http.MethodPost {
		return render(http.StatusOK)
	}

	// What was typed is what the form shows again when one is refused.
	named := Services{
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
	// An empty field leaves the kept password alone; only the reset box lets it go.
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
		if err := ctx.Login.Store().Put(ctx.Request().Context(), servicesKey, &named); err != nil {
			return refuse(err)
		}
	}
	// The cache was read with the servers or password just left.
	ctx.Server.ForgetAccount(ctx.Login.Username())
	ctx.PutNotice(ctx.T("notice.serverssaved"))
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/settings/servers/"+mail.ServerDAV))
}
