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

// DayAnnotation loads a feature's content into the month grid's day labels.
type DayAnnotation struct {
	Name, URL string
}

type CalendarRenderData struct {
	CalendarPageRenderData
	Now                time.Time
	ThisMonth          bool
	Dates              []time.Time
	Weeks              []MonthWeek
	WeekNumbers        bool
	DayAnnotations     []DayAnnotation
	Events             []calendar.Object
	Page               string
	PrevPage, NextPage string
	PrevTime, NextTime time.Time
	// Scope is the account and calendars the page is narrowed to.
	Scope string

	// EventsForDate rows a day's occurrences when the agenda groups by day.
	EventsForDate func(time.Time) []ListEvent
	// ListEvents is the flat agenda's rows, one per multi-day span.
	ListEvents []ListEvent
	// StarView is the star the agenda is narrowed to.
	StarView string
	// FilterRows and GroupRows are the agenda's filter and sort menus.
	GroupRows []web.FilterRow
	// Group is "day" when the agenda is banded by day, else empty.
	Group string
}

type monthView struct {
	loc       *time.Location
	start     time.Time
	monthEnd  time.Time
	now       time.Time
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
		// A month is a span of the reader's calendar, so the comparison is by
		// the span, not the Gregorian month the day number falls in.
		if mv.view != "" && (date.Before(mv.start) || !date.Before(mv.monthEnd)) {
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
	data := &CalendarRenderData{
		CalendarPageRenderData: calendarPageData(ctx, ctx.T("nav.calendar")+": "+ctx.Dates().MonthYear(mv.start), mv.calendars, pimweb.NewCollectionLabels(p.DAV, ctx.Login.Username(), collection, href), mv.view, "", calendarFilters(ctx), agendaRows(ctx, mv), selectForm, invitations, monthQuery(ctx, mv), mv.start),
		Now:                    mv.now,
		ThisMonth:              mv.thisMonth,
		Dates:                  mv.dates,
		Weeks:                  monthWeeks(mv.dates, mv.on),
		WeekNumbers:            !ctx.Reading().NoWeekNumbers,
		Events:                 mv.events,
		Page:                   mv.page,
		PrevPage:               mv.prevPage,
		NextPage:               mv.nextPage,
		PrevTime:               mv.prevTime,
		NextTime:               mv.nextTime,
		Scope:                  web.Query(web.ReadListState(ctx).Keep("account", "cal")),
		StarView: func() string {
			if mv.view == "list" {
				return ""
			}
			return mv.view
		}(),
		GroupRows: groupRows(ctx, mv),
		Group:     mv.group,

		EventsForDate: func(when time.Time) []ListEvent {
			return dayListEvents(mv.on[mv.day(when)], mv.day(when), mv.loc)
		},
	}
	if mv.view != "" && mv.group != "day" {
		month := make([]time.Time, 0, len(mv.dates))
		for _, date := range mv.dates {
			if !date.Before(mv.start) && date.Before(mv.monthEnd) {
				month = append(month, date)
			}
		}
		data.ListEvents = flatListEvents(month, func(when time.Time) []calendar.Occurrence { return mv.on[when] }, mv.loc)
	}
	if mv.view == "" {
		if err := web.Extended(ctx, data); err != nil {
			return err
		}
	}
	return ctx.Render(quickPageStatus(ctx), template, data)
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

	group := ctx.QueryParam("group")
	if group != "" && group != "day" {
		return monthView{}, web.NewError(http.StatusBadRequest, "invalid group")
	}
	now := time.Now().In(loc)
	thisMonth := !now.Before(start) && now.Before(monthEnd)

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
	for i := range dates {
		dates[i] = d
		d = d.AddDate(0, 0, 1)
	}

	// Keys share one loc pointer, since time.Time equality includes the location.
	day := func(t time.Time) time.Time {
		t = t.In(loc)
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
			first, last := occurrenceDays(oc, loc)
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
		monthEnd:  monthEnd,
		now:       now,
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
	star := mv.view
	if star == "list" {
		star = ""
	}
	// Cleared of its star the agenda is still the agenda.
	return web.ViewRows(ctx, star, false, ctx.WithParam("view", "list"))
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
