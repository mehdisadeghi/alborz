package alborzcaldav

import (
	"context"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/collections"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-webdav/caldav"
)

type plugin struct {
	alborz.GoPlugin
	dav  *dav.Provider
	subs *subCache
}

func (p *plugin) client(ctx context.Context, login *alborz.Login) (*caldav.Client, error) {
	return newClient(p.dav.URL(ctx, login), p.dav.HTTPClient(ctx, login))
}

func newPlugin(srv *alborz.Server) (alborz.Plugin, error) {
	provider, err := dav.NewProvider(srv, dav.Kind{
		Name: "caldav", Label: "CalDAV",
		Schemes: [2]string{"caldavs", "caldav+insecure"},
		// Invitations and tasks ticked on a phone should show within minutes.
		Poll:     2 * time.Minute,
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
		GoPlugin: alborz.GoPlugin{Name: "caldav"},
		dav:      provider,
		subs:     newSubCache(srv.Options.CacheFor(subRefreshAfter)),
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

func (p *plugin) registerServerCard() {
	card := p.dav.InjectCard("settings.davcalendar", davAbilities)
	p.Inject("servers.html", card)
	p.Inject("server.html", card)
}

func davAbilities(found dav.Advertised) []alborzbase.Ability {
	return []alborzbase.Ability{
		{Label: "settings.davability3", Hint: "settings.davability3hint", Have: found.Has("3")},
		{Label: "settings.davabilityacl", Hint: "settings.davabilityaclhint", Have: found.Has("access-control")},
		{Label: "settings.davabilitysched", Hint: "settings.davabilityschedhint", Have: found.Has("calendar-auto-schedule")},
		{Label: "settings.davabilitymkcol", Hint: "settings.davabilitymkcolhint", Have: found.Has("extended-mkcol")},
	}
}
