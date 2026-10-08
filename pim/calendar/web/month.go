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

type CalendarRenderData struct {
	CalendarPageRenderData
	Now time.Time
	// Since is the first day the agenda lists: today in the current month, else the first.
	Since time.Time
	// Span is "month" when the agenda was asked for the whole month.
	Span               string
	ThisMonth          bool
	Dates              []time.Time
	Events             []calendar.Object
	Page               string
	View               string
	PrevPage, NextPage string
	PrevTime, NextTime time.Time
	// Scope is the account and calendars the page is narrowed to.
	Scope string

	EventsForDate func(time.Time) []calendar.Occurrence
	// StarView is the star the agenda is narrowed to.
	StarView string
	// FilterRows and GroupRows are the agenda's filter and sort menus.
	GroupRows []web.FilterRow
	// Group is "day" when the agenda is banded by day, else empty.
	Group string
	Sub   func(a, b int) int
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
	events    []calendar.Object
	dates     []time.Time
	on        map[time.Time][]calendar.Occurrence
	day       func(time.Time) time.Time
	unread    []pim.Unread
}

func (mv monthView) shown() []calendar.Occurrence {
	var out []calendar.Occurrence
	for _, date := range mv.dates {
		if mv.view != "" && (date.Month() != mv.start.Month() || date.Before(mv.since)) {
			continue
		}
		out = append(out, mv.on[mv.day(date)]...)
	}
	return out
}

func (p *plugin) month(ctx *web.Context) error {
	mv, err := p.monthView(ctx)
	if err != nil {
		return err
	}
	pimweb.WarnUnread(ctx, mv.unread)
	template := "calendar/month"
	if mv.view != "" {
		template = "calendar/list"
	}
	collection, href := pimweb.Labels(ctx, mv.calendars, "calendar.month", "cal", url.Values{"view": {"list"}})
	invitations, err := p.invitations(ctx)
	if err != nil {
		return err
	}
	selectForm := ""
	if mv.view != "" {
		selectForm = "events-form"
	}
	return ctx.Render(http.StatusOK, template, &CalendarRenderData{
		CalendarPageRenderData: calendarPageData(ctx, ctx.T("nav.calendar")+": "+ctx.Dates().MonthYear(mv.start), mv.calendars, pimweb.NewCollectionLabels(p.DAV, ctx.Login.Username(), collection, href), mv.view, "", calendarFilters(ctx), agendaRows(ctx, mv), selectForm, invitations, monthQuery(ctx, mv), mv.start),
		Now:                    mv.now,
		Since:                  mv.since,
		Span:                   mv.span,
		ThisMonth:              mv.thisMonth,
		Dates:                  mv.dates,
		Events:                 mv.events,
		Page:                   mv.page,
		View:                   mv.view,
		PrevPage:               mv.prevPage,
		NextPage:               mv.nextPage,
		PrevTime:               mv.prevTime,
		NextTime:               mv.nextTime,
		Scope:                  web.Query(pimweb.ListParams(ctx, "account", "cal")),
		StarView: func() string {
			if mv.view == "list" {
				return ""
			}
			return mv.view
		}(),
		GroupRows: groupRows(ctx, mv),
		Group:     mv.group,

		EventsForDate: func(when time.Time) []calendar.Occurrence {
			return mv.on[mv.day(when)]
		},

		Sub: func(a, b int) int {
			return a - b
		},
	})
}

func (p *plugin) monthView(ctx *web.Context) (monthView, error) {
	loc := web.UserLocation(ctx)

	// The month counts in the reader's calendar system.
	cal := ctx.CalendarSystem()
	var start time.Time
	if s := ctx.QueryParam("month"); s != "" {
		var err error
		start, err = cal.ParsePage(s, loc)
		if err != nil {
			return monthView{}, web.NewError(http.StatusBadRequest, err)
		}
	} else {
		start = cal.MonthStart(time.Now().In(loc))
	}
	firstDayOfWeek := ctx.Reading().WeekStart()

	// A star view narrows the agenda; a grid cannot be narrowed and stay a month.
	view := ctx.QueryParam("view")
	if view != "list" && !web.ValidStarView(view) {
		return monthView{}, web.NewError(http.StatusBadRequest, "invalid view")
	}

	// Pad a week each way for adjacent-month days; a fixed window keeps the cache key stable.
	monthEnd := cal.AddMonths(start, 1)
	queryStart := start.AddDate(0, 0, -7)
	queryEnd := monthEnd.AddDate(0, 0, 7)

	// In the current month the agenda starts today; span=month asks for all of it.
	span := ctx.QueryParam("span")
	if span != "" && span != "month" {
		return monthView{}, web.NewError(http.StatusBadRequest, "invalid span")
	}
	group := ctx.QueryParam("group")
	if group != "" && group != "day" {
		return monthView{}, web.NewError(http.StatusBadRequest, "invalid group")
	}
	since := start
	thisMonth := false
	if now := time.Now().In(loc); !now.Before(start) && now.Before(monthEnd) {
		thisMonth = true
		if span != "month" {
			since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		}
	}

	offset := (int(start.Weekday()) - int(firstDayOfWeek) + 7) % 7
	gridStart := start.AddDate(0, 0, -offset)
	daysInMonth := cal.DaysInMonth(start)
	totalCells := offset + daysInMonth
	rows := (totalCells + 6) / 7

	only := pimweb.Only(ctx, "cal")
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return monthView{}, err
	}

	calendarInfos, sites, err := calendar.Visible(ctx.Request().Context(), accounts, ctx.URLAccount(), only, calendar.SupportsEvent, calendar.EventVisibility)
	if err != nil {
		return monthView{}, err
	}
	query := calendar.EventQuery(queryStart, queryEnd)

	events, unread := p.QuerySites(ctx.Request().Context(), sites, &query)

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
	eventMap := make(map[time.Time][]calendar.Occurrence)
	unread = append(unread, eachOccurrence(events,
		func(ev calendar.Object) ([]calendar.Occurrence, error) {
			return calendar.Occurrences(ev, loc, queryStart, queryEnd)
		},
		func(oc calendar.Occurrence) {
			if view != "list" && !web.StarMatches(oc.Star(), view) {
				return
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
		})...)

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
		unread:    unread,
	}, nil
}

func agendaRows(ctx *web.Context, mv monthView) []web.FilterRow {
	if mv.view == "" {
		return nil
	}
	var rows []web.FilterRow
	if mv.thisMonth {
		rows = append(rows,
			web.FilterRow{Label: ctx.T("calendar.fromtoday"), Href: ctx.WithParam("span", ""), Current: mv.span != "month"},
			web.FilterRow{Label: ctx.T("calendar.wholemonth"), Href: ctx.WithParam("span", "month"), Current: mv.span == "month"})
	}
	star := mv.view
	if star == "list" {
		star = ""
	}
	// Cleared of its star the agenda is still the agenda.
	return append(rows, web.ViewRows(ctx, star, false, ctx.WithParam("view", "list"))...)
}

func groupRows(ctx *web.Context, mv monthView) []web.FilterRow {
	if mv.view == "" {
		return nil
	}
	href := ctx.WithParam("group", "day")
	if mv.group == "day" {
		href = ctx.WithParam("group", "")
	}
	return []web.FilterRow{{Label: ctx.T("calendar.groupday"), Href: href, Current: mv.group == "day"}}
}

func (p *plugin) exportMonth(ctx *web.Context) error {
	loc := web.UserLocation(ctx)
	cal := ctx.CalendarSystem()
	from := cal.MonthStart(time.Now().In(loc))
	if s := ctx.QueryParam("month"); s != "" {
		var err error
		if from, err = cal.ParsePage(s, loc); err != nil {
			return web.NewError(http.StatusBadRequest, err)
		}
	}
	return p.exportVisible(ctx, calendar.SupportsEvent, calendar.EventVisibility, []string{"VEVENT"},
		from, cal.AddMonths(from, 1), ctx.T("nav.calendar")+" "+cal.Page(from))
}
