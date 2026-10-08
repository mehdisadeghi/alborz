package web

import (
	"net/http"
	"net/url"
	"sort"
	"time"

	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
)

type CalendarDateRenderData struct {
	CalendarPageRenderData
	Events             []calendar.Occurrence
	PrevPage, NextPage string
}

type dayView struct {
	start     time.Time
	calendars []dav.Collection
	events    []calendar.Occurrence
	accounts  int
	unread    []pim.Unread
}

func (p *plugin) day(ctx *web.Context) error {
	dv, err := p.dayView(ctx)
	if err != nil {
		return err
	}
	pimweb.WarnUnread(ctx, dv.unread)
	collection, href := pimweb.Labels(ctx, dv.calendars, "calendar.month", "cal", url.Values{"view": {"list"}})
	invitations, err := p.invitations(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "calendar/day", &CalendarDateRenderData{
		CalendarPageRenderData: calendarPageData(ctx, ctx.T("nav.calendar")+": "+ctx.Dates().Long(dv.start), dv.calendars, pimweb.NewCollectionLabels(p.DAV, ctx.Login.Username(), collection, href), "", "", nil, nil, "", invitations, dayQuery(ctx, dv.start), dv.start),
		// Named even when the URL omits it, so an opened event knows its list.
		Events:   dv.events,
		PrevPage: dv.start.AddDate(0, 0, -1).Format(datePageLayout),
		NextPage: dv.start.AddDate(0, 0, 1).Format(datePageLayout),
	})
}

func (p *plugin) dayView(ctx *web.Context) (dayView, error) {
	loc := web.UserLocation(ctx)

	var start time.Time
	if s := ctx.QueryParam("date"); s != "" {
		var err error
		start, err = time.ParseInLocation(datePageLayout, s, loc)
		if err != nil {
			return dayView{}, web.NewError(http.StatusBadRequest, err)
		}
	} else {
		now := time.Now().In(loc)
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	}
	end := start.AddDate(0, 0, 1)

	only := pimweb.Only(ctx, "cal")
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return dayView{}, err
	}

	calendarInfos, sites, err := calendar.Visible(ctx.Request().Context(), accounts, ctx.URLAccount(), only, calendar.SupportsEvent, calendar.EventVisibility)
	if err != nil {
		return dayView{}, err
	}
	query := calendar.EventQuery(start, end)

	events, unread := p.QuerySites(ctx.Request().Context(), sites, &query)

	// Keep only what falls in the day; an unexpanded master matched as a whole.
	var shown []calendar.Occurrence
	unread = append(unread, eachOccurrence(events,
		func(ev calendar.Object) ([]calendar.Occurrence, error) {
			return calendar.Occurrences(ev, loc, start, end)
		},
		func(oc calendar.Occurrence) {
			if oc.Start.Before(end) && (oc.End.After(start) || !oc.End.After(oc.Start)) {
				shown = append(shown, oc)
			}
		})...)
	sort.Slice(shown, func(i, j int) bool {
		return shown[i].Start.Before(shown[j].Start)
	})

	return dayView{start: start, calendars: calendarInfos, events: shown, accounts: len(accounts), unread: unread}, nil
}
