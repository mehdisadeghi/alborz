package alborzcaldav

import (
	"net/http"
	"net/url"
	"sort"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/labstack/echo/v4"
)

type CalendarRenderData struct {
	alborz.BaseRenderData
	Time time.Time
	Now  time.Time
	// Since is the first day the agenda lists: today in the current month, else the first.
	Since time.Time
	// Span is "month" when the agenda was asked for the whole month.
	Span               string
	ThisMonth          bool
	Dates              []time.Time
	Calendars          []dav.Collection
	Events             []CalendarObject
	Page               string
	View               string
	PrevPage, NextPage string
	PrevTime, NextTime time.Time
	// ListQuery is carried to an event's page so it can name its neighbours.
	ListQuery string
	// Scope is the account and calendars the page is narrowed to.
	Scope string
	// SelectForm is the form a row's checkbox belongs to; empty where rows offer none.
	SelectForm string

	EventsForDate func(time.Time) []Occurrence
	// CollectionFor is a row's calendar, CollectionHref the agenda narrowed to it.
	CollectionFor  func(account, path string) dav.Collection
	CollectionHref func(account, path string) string
	// AddedBy names who added an event, where not the row's own account.
	AddedBy func(account, path string) string
	// StarView is the star the agenda is narrowed to.
	StarView string
	Filters  []alborz.Filter
	// FilterRows and GroupRows are the agenda's filter and sort menus.
	FilterRows []alborz.FilterRow
	GroupRows  []alborz.FilterRow
	// Group is "day" when the agenda is banded by day, else empty.
	Group string
	Sub   func(a, b int) int

	Invitations *dav.RailInvitations
}

type monthView struct {
	loc       *time.Location
	start     time.Time
	since     time.Time
	now       time.Time
	span      string
	view      string
	group     string
	thisMonth bool
	page      string
	prevPage  string
	nextPage  string
	prevTime  time.Time
	nextTime  time.Time
	accounts  int
	calendars []dav.Collection
	events    []CalendarObject
	dates     []time.Time
	on        map[time.Time][]Occurrence
	day       func(time.Time) time.Time
}

func (mv monthView) shown() []Occurrence {
	var out []Occurrence
	for _, date := range mv.dates {
		if mv.view != "" && (date.Month() != mv.start.Month() || date.Before(mv.since)) {
			continue
		}
		out = append(out, mv.on[mv.day(date)]...)
	}
	return out
}

func (p *plugin) month(ctx *alborz.Context) error {
	mv, err := p.monthView(ctx)
	if err != nil {
		return err
	}
	template := "calendar.html"
	if mv.view != "" {
		template = "calendar-list.html"
	}
	collection, href := dav.Labels(ctx, mv.calendars, "/calendar", "cal", url.Values{"view": {"list"}})
	invitations, err := p.invitations(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, template, &CalendarRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).
			WithTitle(ctx.T("nav.calendar") + ": " + ctx.MonthYearIn(mv.start)),
		Time:      mv.start,
		Now:       mv.now,
		Since:     mv.since,
		Span:      mv.span,
		ThisMonth: mv.thisMonth,
		Calendars: mv.calendars,
		Dates:     mv.dates,
		Events:    mv.events,
		Page:      mv.page,
		View:      mv.view,
		PrevPage:  mv.prevPage,
		NextPage:  mv.nextPage,
		PrevTime:  mv.prevTime,
		NextTime:  mv.nextTime,
		ListQuery: monthQuery(ctx, mv),
		Scope:     alborz.Query(dav.ListParams(ctx, "account", "cal")),
		SelectForm: func() string {
			if mv.view != "" {
				return "events-form"
			}
			return ""
		}(),
		StarView: func() string {
			if mv.view == "list" {
				return ""
			}
			return mv.view
		}(),
		Filters:    calendarFilters(ctx, mv.calendars),
		FilterRows: agendaRows(ctx, mv),
		GroupRows:  groupRows(ctx, mv),
		Group:      mv.group,

		EventsForDate: func(when time.Time) []Occurrence {
			return mv.on[mv.day(when)]
		},

		AddedBy:        p.dav.AddedBy(ctx.Login.Username()),
		CollectionFor:  collection,
		CollectionHref: href,

		Sub: func(a, b int) int {
			return a - b
		},

		Invitations: invitations,
	})
}

func (p *plugin) monthView(ctx *alborz.Context) (monthView, error) {
	loc := alborz.UserLocation(ctx)

	// The month counts in the reader's calendar system.
	cal := ctx.CalendarSystem()
	var start time.Time
	if s := ctx.QueryParam("month"); s != "" {
		var err error
		start, err = cal.ParsePage(s, loc)
		if err != nil {
			return monthView{}, echo.NewHTTPError(http.StatusBadRequest, err)
		}
	} else {
		start = cal.MonthStart(time.Now().In(loc))
	}
	firstDayOfWeek := ctx.Reading().FirstDayOfWeek

	// A star view narrows the agenda; a grid cannot be narrowed and stay a month.
	view := ctx.QueryParam("view")
	if view != "list" && !validStarView(view) {
		return monthView{}, echo.NewHTTPError(http.StatusBadRequest, "invalid view")
	}

	// Pad a week each way for adjacent-month days; a fixed window keeps the cache key stable.
	monthEnd := cal.AddMonths(start, 1)
	queryStart := start.AddDate(0, 0, -7)
	queryEnd := monthEnd.AddDate(0, 0, 7)

	// In the current month the agenda starts today; span=month asks for all of it.
	span := ctx.QueryParam("span")
	if span != "" && span != "month" {
		return monthView{}, echo.NewHTTPError(http.StatusBadRequest, "invalid span")
	}
	group := ctx.QueryParam("group")
	if group != "" && group != "day" {
		return monthView{}, echo.NewHTTPError(http.StatusBadRequest, "invalid group")
	}
	since := start
	thisMonth := false
	if now := time.Now().In(loc); !now.Before(start) && now.Before(monthEnd) {
		thisMonth = true
		if span != "month" {
			since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		}
	}

	offset := (int(start.Weekday()) - firstDayOfWeek + 7) % 7
	gridStart := start.AddDate(0, 0, -offset)
	daysInMonth := cal.DaysInMonth(start)
	totalCells := offset + daysInMonth
	rows := (totalCells + 6) / 7

	only := dav.Only(ctx, "cal")
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return monthView{}, err
	}

	calendarInfos, sites, err := visibleCalendars(ctx.Request().Context(), accounts, ctx.URLAccount(), only, supportsEvent, eventVisibility)
	if err != nil {
		return monthView{}, err
	}
	query := eventQuery(queryStart, queryEnd)

	events, err := querySites(ctx, sites, &query)
	if err != nil {
		return monthView{}, err
	}
	events = append(events, p.subscriptionObjects(ctx.Server.Outgoing.Remote(alborz.RoundTripTimeout), calendarInfos, ctx.URLAccount())...)

	dates := make([]time.Time, rows*7)
	d := gridStart
	for i := 0; i < len(dates); i++ {
		dates[i] = d
		d = d.AddDate(0, 0, 1)
	}

	// Keys share one loc pointer, since time.Time equality includes the location.
	day := func(t time.Time) time.Time {
		t = t.In(loc)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	// An event is drawn on every day it covers; DTEND is exclusive.
	// A DATE names a day, not an instant: its digits are read, not converted to the zone.
	writtenDay := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	gridEnd := gridStart.AddDate(0, 0, rows*7)
	eventMap := make(map[time.Time][]Occurrence)
	for _, ev := range events {
		for _, oc := range occurrences(ev, loc, queryStart, queryEnd) {
			if view != "list" && !alborz.StarMatches(oc.Star(), view) {
				continue
			}
			var first, last time.Time
			if oc.AllDay() {
				first = writtenDay(oc.Start)
				last = first
				if l := writtenDay(oc.End).AddDate(0, 0, -1); l.After(last) {
					last = l
				}
			} else {
				first, last = day(oc.Start), day(oc.Start)
				if oc.End.After(oc.Start) {
					if l := day(oc.End.Add(-time.Nanosecond)); l.After(last) {
						last = l
					}
				}
			}
			if first.Before(gridStart) {
				first = gridStart
			}
			for d := first; !d.After(last) && d.Before(gridEnd); d = d.AddDate(0, 0, 1) {
				eventMap[d] = append(eventMap[d], oc)
			}
		}
	}

	for _, evs := range eventMap {
		sort.Slice(evs, func(i, j int) bool {
			return evs[i].Start.Before(evs[j].Start)
		})
	}

	return monthView{
		loc:       loc,
		start:     start,
		since:     since,
		now:       time.Now().In(loc),
		span:      span,
		view:      view,
		group:     group,
		thisMonth: thisMonth,
		page:      cal.Page(start),
		prevPage:  cal.Page(cal.AddMonths(start, -1)),
		nextPage:  cal.Page(cal.AddMonths(start, 1)),
		prevTime:  cal.AddMonths(start, -1),
		nextTime:  cal.AddMonths(start, 1),
		accounts:  len(accounts),
		calendars: calendarInfos,
		events:    events,
		dates:     dates,
		on:        eventMap,
		day:       day,
	}, nil
}

func agendaRows(ctx *alborz.Context, mv monthView) []alborz.FilterRow {
	if mv.view == "" {
		return nil
	}
	var rows []alborz.FilterRow
	if mv.thisMonth {
		rows = append(rows,
			alborz.FilterRow{Label: ctx.T("calendar.fromtoday"), Href: ctx.WithParam("span", ""), Current: mv.span != "month"},
			alborz.FilterRow{Label: ctx.T("calendar.wholemonth"), Href: ctx.WithParam("span", "month"), Current: mv.span == "month"})
	}
	star := mv.view
	if star == "list" {
		star = ""
	}
	// Cleared of its star the agenda is still the agenda.
	return append(rows, alborz.ViewRows(ctx, star, false, ctx.WithParam("view", "list"))...)
}

func groupRows(ctx *alborz.Context, mv monthView) []alborz.FilterRow {
	if mv.view == "" {
		return nil
	}
	href := ctx.WithParam("group", "day")
	if mv.group == "day" {
		href = ctx.WithParam("group", "")
	}
	return []alborz.FilterRow{{Label: ctx.T("calendar.groupday"), Href: href, Current: mv.group == "day"}}
}

func (p *plugin) exportMonth(ctx *alborz.Context) error {
	loc := alborz.UserLocation(ctx)
	cal := ctx.CalendarSystem()
	from := cal.MonthStart(time.Now().In(loc))
	if s := ctx.QueryParam("month"); s != "" {
		var err error
		if from, err = cal.ParsePage(s, loc); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}
	}
	return p.exportVisible(ctx, supportsEvent, eventVisibility, []string{"VEVENT"},
		from, cal.AddMonths(from, 1), ctx.T("nav.calendar")+" "+cal.Page(from))
}
