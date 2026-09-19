package alborzcaldav

import (
	"embed"

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

// registerServerCard puts the calendar server among the account's
// servers.
func (p *plugin) registerServerCard() {
	card := p.dav.InjectCard("settings.davcalendar", davAbilities)
	p.Inject("servers.html", card)
	p.Inject("server.html", card)
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
