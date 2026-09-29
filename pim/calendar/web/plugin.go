package web

import (
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/web"
)

type plugin struct{ *calendar.Calendars }

// Mount serves calendars and tasks.
func Mount(g *web.Group, st *store.Store) error {
	srv := g.Server()
	c, err := calendar.New(srv.Server, st)
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
