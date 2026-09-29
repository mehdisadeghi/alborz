package calendar

import (
	"context"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-webdav/caldav"
)

// Calendars are every account's calendars and task lists, and the feeds some of them are filled from.
type Calendars struct {
	DAV   *dav.Provider
	feeds *feeds
}

// New finds each served domain's CalDAV server and opens the feeds of the calendars kept in st.
func New(srv *alborz.Server, st *store.Store) (*Calendars, error) {
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
		return nil, err
	}
	return &Calendars{DAV: provider, feeds: openFeeds(srv, st)}, nil
}

// Client is the login's CalDAV client.
func (p *Calendars) Client(ctx context.Context, login *alborz.Login) (*caldav.Client, error) {
	return newClient(dav.Base(), p.DAV.HTTPClient(ctx, login))
}

type plugin struct{ *Calendars }

// Mount serves calendars and tasks.
func Mount(g *web.Group, st *store.Store) error {
	srv := g.Server()
	c, err := New(srv.Server, st)
	if err != nil {
		return err
	}
	p := &plugin{c}

	registerRoutes(p, g)
	p.registerScheduling(g)
	srv.OnMessage = append(srv.OnMessage, p.collectionPage().ShowOffer(p.DAV))
	srv.OnServers = append(srv.OnServers, p.DAV.Cards("settings.davcalendar", davAbilities))
	srv.OnAccountReady = append(srv.OnAccountReady, p.warm)

	return nil
}

func davAbilities(found dav.Advertised) []web.Ability {
	return []web.Ability{
		{Label: "settings.davability3", Hint: "settings.davability3hint", Have: found.Has("3")},
		{Label: "settings.davabilityacl", Hint: "settings.davabilityaclhint", Have: found.Has("access-control")},
		{Label: "settings.davabilitysched", Hint: "settings.davabilityschedhint", Have: found.Has("calendar-auto-schedule")},
		{Label: "settings.davabilitymkcol", Hint: "settings.davabilitymkcolhint", Have: found.Has("extended-mkcol")},
	}
}
