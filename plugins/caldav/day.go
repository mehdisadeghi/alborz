package alborzcaldav

import (
	"net/http"
	"net/url"
	"sort"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/labstack/echo/v4"
)

type CalendarDateRenderData struct {
	alborz.BaseRenderData
	ListQuery string
	// SelectForm is empty here: a day is a page about a day, and the
	// list it shows is the agenda's job to act on.
	SelectForm         string
	Time               time.Time
	Calendars          []dav.Collection
	Events             []Occurrence
	PrevPage, NextPage string

	CollectionFor  func(account, path string) dav.Collection
	CollectionHref func(account, path string) string
	// AddedBy names who added an event kept here, where not the row's
	// own account.
	AddedBy func(account, path string) string
}

// dayView is the day page as its handler works it out: the day it
// draws and what falls on it, in the order it is shown.
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
	return ctx.Render(http.StatusOK, "calendar-date.html", &CalendarDateRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).
			WithTitle(ctx.T("nav.calendar") + ": " + ctx.LongDateIn(dv.start)),
		// The day is named even where the URL left it out, or an event
		// opened from today would not know which list it came from.
		ListQuery:      dayQuery(ctx, dv.start),
		Time:           dv.start,
		Calendars:      dv.calendars,
		Events:         dv.events,
		PrevPage:       dv.start.AddDate(0, 0, -1).Format(datePageLayout),
		NextPage:       dv.start.AddDate(0, 0, 1).Format(datePageLayout),
		AddedBy:        p.dav.AddedBy(ctx.Session.Username()),
		CollectionFor:  collection,
		CollectionHref: href,
	})
}

func (p *plugin) dayView(ctx *alborz.Context) (dayView, error) {
	loc := alborzbase.UserLocation(ctx)

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

	calendarInfos, sites, err := visibleCalendars(accounts, ctx.URLAccount(), only, supportsEvent, eventVisibility)
	if err != nil {
		return dayView{}, err
	}
	query := eventQuery(start, end)

	events, err := querySites(ctx, sites, &query)
	if err != nil {
		return dayView{}, err
	}
	events = append(events, subscriptionObjects(calendarInfos, ctx.URLAccount())...)

	// The same two shapes as the month grid: expanded instances, or a
	// master still carrying its rule. Only what falls in the day is
	// kept, since a server that ignores the expand request filters
	// on the object rather than on the instance.
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
