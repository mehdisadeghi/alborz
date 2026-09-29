package calendar

import (
	"context"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/settings"
	"github.com/emersion/go-webdav/caldav"
)

type plugin struct {
	alborz.Router
	dav   *dav.Provider
	feeds *feeds
}

func (p *plugin) client(ctx context.Context, login *alborz.Login) (*caldav.Client, error) {
	return newClient(dav.Base(), p.dav.HTTPClient(ctx, login))
}

// Mount serves calendars and tasks.
func Mount(srv *alborz.Server, st *store.Store) error {
	provider, err := dav.NewProvider(srv, st, dav.Kind{
		Name: "caldav", Label: "CalDAV",
		Schemes: [2]string{"caldavs", "caldav+insecure"},
		// Invitations and tasks ticked on a phone should show within minutes.
		Poll:     2 * time.Minute,
		Discover: caldav.DiscoverContextURL,
		Own:      func(s dav.Services) string { return s.CalDAV },
		Holds:    store.Calendar,
		FindHome: findHome,
		List:     listCalendars,
		Make:     makeCalendar,
		Change:   changeCalendar,
		Unnamed:  "calendar",
	})
	if err != nil {
		return err
	}

	feeds, err := openFeeds(srv)
	if err != nil {
		return err
	}
	p := &plugin{
		Router: srv.Router(),
		dav:    provider,
		feeds:  feeds,
	}

	registerRoutes(p)
	p.registerScheduling(srv)
	srv.OnMessage = append(srv.OnMessage, p.collectionPage().ShowOffer(p.dav))
	srv.OnServers = append(srv.OnServers, p.dav.Cards("settings.davcalendar", davAbilities))
	srv.OnAccountReady = append(srv.OnAccountReady, p.warm)

	return nil
}

func davAbilities(found dav.Advertised) []settings.Ability {
	return []settings.Ability{
		{Label: "settings.davability3", Hint: "settings.davability3hint", Have: found.Has("3")},
		{Label: "settings.davabilityacl", Hint: "settings.davabilityaclhint", Have: found.Has("access-control")},
		{Label: "settings.davabilitysched", Hint: "settings.davabilityschedhint", Have: found.Has("calendar-auto-schedule")},
		{Label: "settings.davabilitymkcol", Hint: "settings.davabilitymkcolhint", Have: found.Has("extended-mkcol")},
	}
}
