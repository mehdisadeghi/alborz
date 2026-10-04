package web

import (
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	pimweb "git.mehdix.org/alborz/pim/web"
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
	srv.Searchers = append(srv.Searchers,
		web.Searcher{Kind: "events", Answers: []string{web.StarKey}, Rows: "events-found", Search: p.foundEvents},
		web.Searcher{Kind: "tasks", Answers: []string{web.StarKey}, Rows: "tasks-found", Search: p.foundTasks})
	p.registerScheduling(g)
	web.Extend(srv, p.collectionPage().ShowOffer(p.DAV))
	srv.OnServers = append(srv.OnServers, pimweb.Cards(p.DAV, "settings.davcalendar", davAbilities))
	srv.OnAccountReady = append(srv.OnAccountReady, p.warm)
	srv.Status = append(srv.Status, web.Segment{Name: "calendar", Show: p.showToday})
	srv.Schedule.Add(srv.Options.ReminderTick, p.remind(srv))

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
