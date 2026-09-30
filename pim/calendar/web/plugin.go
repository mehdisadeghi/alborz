package web

import (
	"html/template"
	"slices"
	"strings"

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
	p.registerScheduling(g)
	web.Extend(srv, p.collectionPage().ShowOffer(p.DAV))
	srv.OnServers = append(srv.OnServers, pimweb.Cards(p.DAV, "settings.davcalendar", davAbilities))
	srv.OnAccountReady = append(srv.OnAccountReady, p.warm)
	srv.Status = append(srv.Status, web.Segment{Name: "calendar", Show: p.showToday})

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

// warnUnread logs what a page left out and names the calendars it leaves incomplete.
func warnUnread(ctx *web.Context, unread []calendar.Unread) {
	var names, marked []string
	for _, u := range unread {
		ctx.Logger().Printf("calendar: %s: %v", u.Calendar, u.Err)
		if !slices.Contains(names, u.Calendar) {
			names = append(names, u.Calendar)
			marked = append(marked, web.NoticeName(u.Calendar))
		}
	}
	if len(names) == 0 {
		return
	}
	ctx.Notify(web.Notice{Kind: web.NoticeWarning,
		Text:   ctx.Tf("notice.calendarunread", len(names), strings.Join(names, ", ")),
		Markup: template.HTML(ctx.Tf("notice.calendarunread", len(names), strings.Join(marked, ", ")))})
}
