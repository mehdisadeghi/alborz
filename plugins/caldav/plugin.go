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
	alborz.Router
	dav  *dav.Provider
	subs *subCache
}

func (p *plugin) client(ctx context.Context, login *alborz.Login) (*caldav.Client, error) {
	return newClient(p.dav.URL(ctx, login), p.dav.HTTPClient(ctx, login))
}

// Mount serves calendars and tasks.
func Mount(srv *alborz.Server) error {
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
		return err
	}

	p := &plugin{
		Router: srv.Router(),
		dav:    provider,
		subs:   newSubCache(srv.Options.CacheFor(subRefreshAfter)),
	}

	registerRoutes(p)
	p.registerScheduling(srv)
	srv.OnMessage = append(srv.OnMessage, p.collectionPage().ShowOffer(p.dav))
	srv.OnServers = append(srv.OnServers, p.dav.Cards("settings.davcalendar", davAbilities))
	srv.OnAccountReady = append(srv.OnAccountReady, p.warm)

	return nil
}

func davAbilities(found dav.Advertised) []alborzbase.Ability {
	return []alborzbase.Ability{
		{Label: "settings.davability3", Hint: "settings.davability3hint", Have: found.Has("3")},
		{Label: "settings.davabilityacl", Hint: "settings.davabilityaclhint", Have: found.Has("access-control")},
		{Label: "settings.davabilitysched", Hint: "settings.davabilityschedhint", Have: found.Has("calendar-auto-schedule")},
		{Label: "settings.davabilitymkcol", Hint: "settings.davabilitymkcolhint", Have: found.Has("extended-mkcol")},
	}
}
