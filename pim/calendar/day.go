package calendar

import (
	"net/http"
	"net/url"
	"sort"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/labstack/echo/v4"
)

type CalendarDateRenderData struct {
	alborz.BaseRenderData
	ListQuery string
	// SelectForm is empty: the agenda, not the day page, acts on the list.
	SelectForm         string
	Time               time.Time
	Calendars          []dav.Collection
	Events             []Occurrence
	PrevPage, NextPage string

	CollectionFor  func(account, path string) dav.Collection
	CollectionHref func(account, path string) string
	// AddedBy names who added an event, where not the row's own account.
	AddedBy func(account, path string) string

	Invitations *dav.RailInvitations
}

type dayView struct {
	start     time.Time
	calendars []dav.Collection
	events    []Occurrence
	accounts  int
}

func (p *plugin) day(ctx *alborz.Context) error {
	dv, err := p.dayView(ctx)
	if err != nil {
		return err
	}
	collection, href := dav.Labels(ctx, dv.calendars, "/calendar", "cal", url.Values{"view": {"list"}})
	invitations, err := p.invitations(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "calendar-date.html", &CalendarDateRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).
			WithTitle(ctx.T("nav.calendar") + ": " + ctx.LongDateIn(dv.start)),
		// Named even when the URL omits it, so an opened event knows its list.
		ListQuery:      dayQuery(ctx, dv.start),
		Time:           dv.start,
		Calendars:      dv.calendars,
		Events:         dv.events,
		PrevPage:       dv.start.AddDate(0, 0, -1).Format(datePageLayout),
		NextPage:       dv.start.AddDate(0, 0, 1).Format(datePageLayout),
		AddedBy:        p.dav.AddedBy(ctx.Login.Username()),
		CollectionFor:  collection,
		CollectionHref: href,
		Invitations:    invitations,
	})
}

func (p *plugin) dayView(ctx *alborz.Context) (dayView, error) {
	loc := alborz.UserLocation(ctx)

	var start time.Time
	if s := ctx.QueryParam("date"); s != "" {
		var err error
		start, err = time.ParseInLocation(datePageLayout, s, loc)
		if err != nil {
			return dayView{}, echo.NewHTTPError(http.StatusBadRequest, err)
		}
	} else {
		now := time.Now().In(loc)
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	}
	end := start.AddDate(0, 0, 1)

	only := dav.Only(ctx, "cal")
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return dayView{}, err
	}

	calendarInfos, sites, err := visibleCalendars(ctx.Request().Context(), accounts, ctx.URLAccount(), only, supportsEvent, eventVisibility)
	if err != nil {
		return dayView{}, err
	}
	query := eventQuery(start, end)

	events, err := querySites(ctx, sites, &query)
	if err != nil {
		return dayView{}, err
	}
	events = append(events, p.subscriptionObjects(ctx.Server.Outgoing.Remote(alborz.RoundTripTimeout), calendarInfos, ctx.URLAccount())...)

	// Keep only what falls in the day; an unexpanded master matched as a whole.
	var shown []Occurrence
	for _, ev := range events {
		for _, oc := range occurrences(ev, loc, start, end) {
			if oc.Start.Before(end) && (oc.End.After(start) || !oc.End.After(oc.Start)) {
				shown = append(shown, oc)
			}
		}
	}
	sort.Slice(shown, func(i, j int) bool {
		return shown[i].Start.Before(shown[j].Start)
	})

	return dayView{start: start, calendars: calendarInfos, events: shown, accounts: len(accounts)}, nil
}
