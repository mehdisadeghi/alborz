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

type CalendarRenderData struct {
	alborz.BaseRenderData
	Time time.Time
	Now  time.Time
	// Since is the first day the agenda lists: today when today falls in
	// the month in front of you, the first of the month otherwise. The
	// grid shows a month; the agenda answers what is next.
	Since time.Time
	// Span is "month" when the agenda was asked for the whole of it, and
	// ThisMonth says whether the choice exists at all - a month with no
	// today in it has only one answer.
	Span               string
	ThisMonth          bool
	Dates              []time.Time
	Calendars          []dav.Collection
	Events             []CalendarObject
	Page               string // the shown month as a ?month= value
	View               string // "" for the month grid, "list" for the agenda
	PrevPage, NextPage string
	PrevTime, NextTime time.Time
	// ListQuery is what the page's own URL says about which events it
	// holds and in what order, carried on to an event's page so that
	// page can name the events either side of it.
	ListQuery string
	// Scope is the account and the calendars the page is narrowed to,
	// which every link in its toolbar keeps: another month or the
	// other view of the same calendars.
	Scope string
	// SelectForm is the form a row's checkbox belongs to where the page
	// has one; empty where it has none, and then no row offers a box
	// and no column is kept for one. The agenda is a list like the
	// others; the month grid is not a list at all.
	SelectForm string

	EventsForDate func(time.Time) []Occurrence
	// CollectionFor is the calendar holding a row's event, and
	// CollectionHref the agenda narrowed to it.
	CollectionFor  func(account, path string) dav.Collection
	CollectionHref func(account, path string) string
	// AddedBy names who added an event kept here, where not the row's
	// own account.
	AddedBy func(account, path string) string
	// StarView is the star the agenda is narrowed to, for the rail;
	// empty for the grid and the plain agenda.
	StarView string
	Filters  []alborz.Filter
	// FilterRows is the agenda's filter menu and GroupRows its sort
	// menu; both empty on the grid, which narrows and groups nothing.
	FilterRows []alborz.FilterRow
	GroupRows  []alborz.FilterRow
	// Group is "day" when the agenda is banded by day, else empty.
	Group string
	Sub   func(a, b int) int
}

// monthView is the month page as its handler works it out: the window
// it asked the server for, the days it draws and what falls on each of
// them. A single event's page builds the same view, to know which
// events stand either side of it in the one the reader came from.
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
	// on holds each drawn day's occurrences, in the order they are
	// shown; day keys it, in the display timezone.
	on  map[time.Time][]Occurrence
	day func(time.Time) time.Time
}

// shown is the page's occurrences in the order a reader meets them: by
// day across the drawn dates, and the agenda's days only where the
// agenda is what is drawn.
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
		Scope:     alborz.AddressQuery(dav.ListParams(ctx, "account", "cal")),
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

		AddedBy:        p.dav.AddedBy(ctx.Session.Username()),
		CollectionFor:  collection,
		CollectionHref: href,

		Sub: func(a, b int) int {
			// Why isn't this built-in, come on Go
			return a - b
		},
	})
}

func (p *plugin) monthView(ctx *alborz.Context) (monthView, error) {
	loc := alborzbase.UserLocation(ctx)

	// The month is the reader's calendar's month: its bounds, its page
	// name and its length come from the system they count in.
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

	// "list" is the agenda; a star view is the agenda narrowed to the
	// events marked so, since a grid cannot be narrowed and stay a month.
	view := ctx.QueryParam("view")
	if view != "list" && !validStarView(view) {
		return monthView{}, echo.NewHTTPError(http.StatusBadRequest, "invalid view")
	}

	// Pad a week each way: the grid shows adjacent-month days, and a
	// fixed window keeps the cache key stable.
	monthEnd := cal.AddMonths(start, 1)
	queryStart := start.AddDate(0, 0, -7)
	queryEnd := monthEnd.AddDate(0, 0, 7)

	// The agenda answers "what is next", so in the month you are
	// living in it starts at today; span=month asks for the whole of
	// it, the same range the grid draws.
	span := ctx.QueryParam("span")
	if span != "" && span != "month" {
		return monthView{}, echo.NewHTTPError(http.StatusBadRequest, "invalid span")
	}
	// Rows by default; group=day bands them under the day they fall on.
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

	calendarInfos, sites, err := visibleCalendars(accounts, ctx.URLAccount(), only, supportsEvent, eventVisibility)
	if err != nil {
		return monthView{}, err
	}
	query := eventQuery(queryStart, queryEnd)

	events, err := querySites(ctx, sites, &query)
	if err != nil {
		return monthView{}, err
	}
	events = append(events, subscriptionObjects(calendarInfos, ctx.URLAccount())...)

	dates := make([]time.Time, rows*7)
	d := gridStart
	for i := 0; i < len(dates); i++ {
		dates[i] = d
		d = d.AddDate(0, 0, 1)
	}

	// Bucket by calendar day in the display timezone; both sides of
	// the map must build keys with the same loc pointer, as time.Time
	// map equality includes the location.
	day := func(t time.Time) time.Time {
		t = t.In(loc)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	// An event covers every day between its start and its end, and
	// the grid has to show it on each of them: bucketing on the
	// start alone put a two-day conference on the first day and
	// nowhere else. DTEND is exclusive (RFC 5545 3.8.2.2) both for
	// an all-day event, whose end is the day after its last, and for
	// a timed one ending at midnight, which belongs to the day
	// before - so the last day is the one the instant before the end
	// falls in. The span is clipped to the grid, since an event may
	// run for years and the map only holds what is drawn.
	// A whole-day event is written as DATE (RFC 5545 3.3.4), which
	// names a calendar day and carries neither a time nor a zone.
	// Parsed it can only arrive as an instant - midnight UTC - and
	// putting that instant through the display zone moves it: east
	// of UTC the last day lands on the day after, which is why a
	// one-day event was drawn on two while the day page, which asks
	// the server, showed it on one. The written date is the answer,
	// so it is read as digits rather than converted.
	writtenDay := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	gridEnd := gridStart.AddDate(0, 0, rows*7)
	eventMap := make(map[time.Time][]Occurrence)
	for _, ev := range events {
		for _, oc := range occurrences(ev, loc, queryStart, queryEnd) {
			if view != "list" && !alborzbase.StarMatches(oc.Star(), view) {
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

// agendaRows is the agenda's filter menu: the slice of the month in
// front of you, where today falls in it, then the star views. The grid
// offers none, having nothing to narrow.
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
	return append(rows, alborzbase.ViewRows(ctx, star, false, ctx.WithParam("view", "list"))...)
}

// groupRows is the agenda's sort menu, which for now holds the one
// way to group it.
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

// The object exactly as the server stores it. Nothing here parses
// it: a raw view is only useful while it is verbatim.
// exportMonth hands the visible calendars' events of the month shown
// back as one file, named for the month.
func (p *plugin) exportMonth(ctx *alborz.Context) error {
	loc := alborzbase.UserLocation(ctx)
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
