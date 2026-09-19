package alborzcaldav

import (
	"embed"
	"strings"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/collections"
	"git.mehdix.org/alborz/plugins/dav"
	"git.mehdix.org/alborz/plugins/davcache"
	"github.com/emersion/go-webdav/caldav"
)

//go:embed all:public
var public embed.FS

type plugin struct {
	alborz.GoPlugin
	dav *dav.Provider
}

func (p *plugin) client(session *alborz.Session) (*caldav.Client, error) {
	u, ok := p.dav.URL(session)
	if !ok {
		return nil, errNoCalendar
	}
	return newClient(u, p.dav.HTTPClient(session))
}

func newPlugin(srv *alborz.Server) (alborz.Plugin, error) {
	provider, err := dav.NewProvider(srv, dav.Kind{
		Name: "caldav", Label: "CalDAV",
		Schemes: [2]string{"caldavs", "caldav+insecure"},
		// Invitations and tasks ticked on a phone should show within
		// minutes.
		Poll:     davcache.DefaultPoll,
		Discover: caldav.DiscoverContextURL,
		Own:      func(s alborz.Services) string { return s.CalDAV },
		Holds:    collections.Calendar,
		FindHome: findHome,
		List:     listCalendars,
		Make:     doMkcalendar,
		Unnamed:  "calendar",
	})
	if err != nil {
		return nil, err
	}

	p := &plugin{
		GoPlugin: alborz.GoPlugin{Name: "caldav", Files: public},
		dav:      provider,
	}
	p.EnabledFunc = provider.Enabled
	p.CloseFunc = provider.Close

	registerRoutes(p)
	p.registerScheduling()
	p.registerServerCard()
	srv.OnAccountReady = append(srv.OnAccountReady, p.warm)

	return p.Plugin(), nil
}

func init() {
	alborz.RegisterPluginLoader(func(s *alborz.Server) ([]alborz.Plugin, error) {
		p, err := newPlugin(s)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, nil
		}
		return []alborz.Plugin{p}, err
	})
}

// registerServerCard answers for this account's calendar server on the
// Servers page, in the same shape mail answers for its own: where it
// is, what it calls itself, and which of the protocol it claims. A
// calendar that behaves oddly is somebody else's deployment, and this
// is where a bug report starts.
func (p *plugin) registerServerCard() {
	p.Inject("servers.html", func(ctx *alborz.Context, data alborz.RenderData) error {
		servers, ok := data.(*alborzbase.ServersRenderData)
		if !ok || ctx.Session == nil {
			return nil
		}
		base, ok := p.dav.URL(ctx.Session)
		if !ok {
			return nil
		}
		// What is kept here syncs to a phone like any DAV server's, and
		// this is the address the phone is given.
		if p.dav.KeepsHere() {
			servers.More = append(servers.More, alborzbase.ServerCard{
				Title: ctx.T("settings.davhere"),
				Rows: []map[string]any{
					{"label": ctx.T("settings.davhereaddress"), "value": ctx.Scheme() + "://" + ctx.Request().Host + collections.Prefix + "/"},
					{"label": ctx.T("settings.davhereuser"), "value": ctx.Session.Username()},
				},
			})
		}
		card := alborzbase.ServerCard{Title: ctx.T("settings.davcalendar")}
		found, err := dav.Describe(ctx.Request().Context(), p.dav.HTTPClient(ctx.Session), base)
		if err != nil {
			// A server that did not answer is worth saying so about;
			// the page is not the place to fail over it.
			card.Rows = []map[string]any{
				{"label": ctx.T("settings.serverhost"), "value": p.dav.Host(ctx)},
				{"label": ctx.T("settings.serverunreachable"), "value": err.Error()},
			}
			servers.More = append(servers.More, card)
			return nil
		}
		card.Rows = []map[string]any{
			{"label": ctx.T("settings.serverhost"), "value": p.dav.Host(ctx)},
			{"label": ctx.T("settings.serversoftware"), "value": found.Software},
			{"label": ctx.T("settings.davcompliance"), "value": strings.Join(found.Compliance, ", ")},
		}
		card.Abilities = davAbilities(found)
		servers.More = append(servers.More, card)
		return nil
	})
}

// davAbilities names the classes that change what alborz can offer,
// each with a line saying what it changes; the rest of the DAV header
// is shown as the server wrote it and explained by nobody.
func davAbilities(found dav.Advertised) []alborzbase.Ability {
	return []alborzbase.Ability{
		{Label: "settings.davability3", Hint: "settings.davability3hint", Have: found.Has("3")},
		{Label: "settings.davabilityacl", Hint: "settings.davabilityaclhint", Have: found.Has("access-control")},
		{Label: "settings.davabilitysched", Hint: "settings.davabilityschedhint", Have: found.Has("calendar-auto-schedule")},
		{Label: "settings.davabilitymkcol", Hint: "settings.davabilitymkcolhint", Have: found.Has("extended-mkcol")},
	}
}
