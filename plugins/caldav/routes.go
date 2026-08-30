package alborzcaldav

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// NewCalendarRenderData drives the create-a-calendar form.
type NewCalendarRenderData struct {
	alborz.BaseRenderData
	Accounts []alborz.Account
	Name     string
	Account  string
	Color    string
	Holds    string // "events", "tasks" or "both"
	ForTasks bool   // reached from the tasks rail
	Next     string // the list it was opened from
	Error    string
}

// handleCreateCalendar adds a collection to the chosen account. The
// component set is the one decision a calendar cannot change later on
// most servers, so it is asked here rather than assumed.
func handleCreateCalendar(p *plugin) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		// A task list is a calendar whose component set holds VTODO;
		// RFC 4791 knows no other kind of collection. One form makes
		// both, entered from whichever rail wants one.
		forTasks := ctx.QueryParam("for") == "tasks"
		holds := "both"
		if forTasks {
			holds = "tasks"
		}
		title := "calendar.newcalendar"
		if forTasks {
			title = "tasks.newlist"
		}
		data := &NewCalendarRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T(title)),
			Accounts:       ctx.Accounts(),
			Account:        ctx.Session.Username(),
			Color:          "#3366cc",
			Holds:          holds,
			ForTasks:       forTasks,
			Next:           ctx.FormValue("next"),
		}
		if ctx.Request().Method != http.MethodPost {
			return ctx.Render(http.StatusOK, "create-calendar.html", data)
		}

		data.Name = strings.TrimSpace(ctx.FormValue("name"))
		data.Color = ctx.FormValue("color")
		// The tasks entrance offers no choice, so it cannot be posted one.
		if h := ctx.FormValue("holds"); !forTasks && (h == "events" || h == "tasks" || h == "both") {
			data.Holds = h
		}
		if account := ctx.FormValue("account"); account != "" {
			data.Account = account
		}
		if data.Name == "" {
			data.Error = ctx.T("form.nameneeded")
			return ctx.Render(http.StatusUnprocessableEntity, "create-calendar.html", data)
		}

		session := ctx.SessionFor(data.Account)
		if session == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
		}
		var components []string
		if data.Holds != "tasks" {
			components = append(components, "VEVENT")
		}
		if data.Holds != "events" {
			components = append(components, "VTODO")
		}
		if err := p.createCalendar(ctx.Request().Context(), session, data.Name, components, data.Color); err != nil {
			data.Error = err.Error()
			return ctx.Render(http.StatusUnprocessableEntity, "create-calendar.html", data)
		}
		// Back to the rail it was asked for, when the new collection
		// shows there; a task list never appears under calendars.
		if data.Holds == "tasks" || (data.ForTasks && data.Holds == "both") {
			return ctx.Redirect(http.StatusFound, ctx.NextOr("/tasks"))
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr("/calendar"))
	}
}

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
	Calendars          []CalendarInfo
	Events             []CalendarObject
	Page               string // the shown month as a ?month= value
	View               string // "" for the month grid, "list" for the agenda
	PrevPage, NextPage string
	PrevTime, NextTime time.Time

	EventsForDate func(time.Time) []CalendarObject
	ColorForPath  func(account, path string) string
	OwnerLabel    func(account, path string) string
	Sub           func(a, b int) int
}

type CalendarDateRenderData struct {
	alborz.BaseRenderData
	Time               time.Time
	Calendars          []CalendarInfo
	Events             []CalendarObject
	PrevPage, NextPage string

	ColorForPath func(account, path string) string
	OwnerLabel   func(account, path string) string
}

type EventRenderData struct {
	alborz.BaseRenderData
	Calendar *CalendarInfo
	Event    CalendarObject
}

type UpdateEventRenderData struct {
	alborz.BaseRenderData
	AccountCals    []CalendarGroup
	Calendar       *CalendarInfo
	CalendarObject *caldav.CalendarObject // nil if creating a new event
	Event          *ical.Event

	// The two shapes the form offers, filled whichever one applies so
	// that ticking the box does not lose what was already typed.
	AllDay    bool
	StartTime string // datetime-local
	EndTime   string
	StartDate string // date, and the last day rather than the day after
	EndDate   string

	// Error is shown as an alert on the form just submitted: invalid
	// input is answered by the page itself, never by a status page.
	Error string
}

type Settings struct {
	CalendarFilter   bool
	VisibleCalendars []string
	TaskFilter       bool
	VisibleTasks     []string
	ShowCompleted    bool
}

type TasksRenderData struct {
	alborz.BaseRenderData
	Calendars []CalendarInfo
	Tasks     []TaskRow

	// True when every account shows completed tasks; the single aside
	// toggle writes all of them.
	ShowCompleted bool
	Query         string
	Sort          string
	SortDir       string
}

// TaskRow is the flat, table-shaped representation shared by the task
// list and its sort controls. Calendar ownership stays explicit instead
// of being encoded as nested visual groups.
type TaskRow struct {
	Task      TaskObject
	Calendar  CalendarInfo
	Summary   string
	Status    string
	Due       time.Time
	Completed bool
}

type TaskRenderData struct {
	alborz.BaseRenderData
	Calendar *CalendarInfo
	Task     TaskObject
}

type UpdateTaskRenderData struct {
	alborz.BaseRenderData
	AccountCals    []CalendarGroup
	Calendar       *CalendarInfo
	CalendarObject *caldav.CalendarObject
	Todo           *ical.Component
	Error          string
}

const (
	monthPageLayout             = "2006-01"
	datePageLayout              = "2006-01-02"
	maxCalendarQueryConcurrency = 4
	settingsKey                 = "caldav.settings"
)

// getCalendarObject fetches one event or task without go-webdav's
// response parsing; see getAddressObject in the carddav plugin for why
// the ETag makes that fail against Nextcloud.
func getCalendarObject(ctx *alborz.Context, c *caldav.Client, path string) (*caldav.CalendarObject, error) {
	body, err := c.Open(ctx.Request().Context(), path)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	cal, err := ical.NewDecoder(body).Decode()
	if err != nil {
		return nil, err
	}
	return &caldav.CalendarObject{Path: path, Data: cal}, nil
}

func parseObjectPath(s string) (string, error) {
	p, err := url.PathUnescape(s)
	if err != nil {
		err = fmt.Errorf("failed to parse path: %v", err)
		return "", echo.NewHTTPError(http.StatusBadRequest, err)
	}
	return p, nil
}

func parseDateTime(s string, loc *time.Location) (time.Time, error) {
	t, err := time.ParseInLocation(inputDateTimeLayout, s, loc)
	if err != nil {
		err = fmt.Errorf("malformed datetime: %v", err)
		return time.Time{}, echo.NewHTTPError(http.StatusBadRequest, err)
	}
	return t, nil
}

// fillEventForm gives the form both shapes of the same event: the times
// it has, and the days it covers. Ticking the box then costs nothing
// that was already entered. The last day is shown, not the day after -
// DTEND is exclusive in iCalendar and inclusive in every head.
func fillEventForm(d *UpdateEventRenderData, loc *time.Location) {
	start, _ := d.Event.DateTimeStart(loc)
	end, _ := d.Event.DateTimeEnd(loc)
	if prop := d.Event.Props.Get(ical.PropDateTimeStart); prop != nil {
		d.AllDay = prop.ValueType() == ical.ValueDate
	}
	if start.IsZero() {
		start = time.Now().In(loc)
	}
	if !end.After(start) {
		end = start.Add(time.Hour)
	}
	d.StartTime = start.Format(inputDateTimeLayout)
	d.EndTime = end.Format(inputDateTimeLayout)
	d.StartDate = start.Format(datePageLayout)
	last := end
	if d.AllDay {
		last = end.AddDate(0, 0, -1)
		if last.Before(start) {
			last = start
		}
	}
	d.EndDate = last.Format(datePageLayout)
}

// parseDate reads a bare day from a date input, which is what an
// all-day event is given in.
func parseDate(s string, loc *time.Location) (time.Time, error) {
	t, err := time.ParseInLocation(datePageLayout, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("malformed date: %v", err)
	}
	return t, nil
}

func loadSettings(store alborz.Store) (*Settings, error) {
	settings := &Settings{}
	if err := store.Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	return settings, nil
}

func getFirstTodo(cal *ical.Calendar) *ical.Component {
	for _, child := range cal.Children {
		if child.Name == ical.CompToDo {
			return child
		}
	}
	return nil
}

func calendarByPath(calendars []CalendarInfo, path string) *CalendarInfo {
	for i := range calendars {
		if calendars[i].Path == path {
			return &calendars[i]
		}
	}
	return nil
}

func registerRoutes(p *plugin) {
	// A missing calendar is a state to explain, not an error: every
	// route in the section answers with the account named.
	guard := func(h func(*alborz.Context) error) func(*alborz.Context) error {
		return func(ctx *alborz.Context) error {
			err := h(ctx)
			if errors.Is(err, errNoCalendar) {
				return alborz.RenderInfo(ctx, http.StatusOK,
					fmt.Sprintf(ctx.T("calendar.unconfigured"), ctx.Session.Username()))
			}
			return err
		}
	}
	GET := func(path string, h func(*alborz.Context) error) { p.GoPlugin.GET(path, guard(h)) }
	POST := func(path string, h func(*alborz.Context) error) { p.GoPlugin.POST(path, guard(h)) }
	POST("/calendar", func(ctx *alborz.Context) error {
		settings, err := loadSettings(ctx.Session.Store())
		if err != nil {
			return fmt.Errorf("failed to load CalDAV settings: %v", err)
		}
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		settings.CalendarFilter = true
		settings.VisibleCalendars = params["cal"]
		if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
			return fmt.Errorf("failed to save CalDAV settings: %v", err)
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr("/calendar"))
	})

	POST("/tasks", func(ctx *alborz.Context) error {
		settings, err := loadSettings(ctx.Session.Store())
		if err != nil {
			return fmt.Errorf("failed to load CalDAV settings: %v", err)
		}
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		settings.TaskFilter = true
		settings.VisibleTasks = params["cal"]
		if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
			return fmt.Errorf("failed to save CalDAV settings: %v", err)
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr("/tasks"))
	})

	// One toggle for the pooled page: a view option that reads as one
	// control writes every account's setting.
	POST("/tasks/show-completed", func(ctx *alborz.Context) error {
		on := ctx.FormValue("show-completed") == "1"
		for _, session := range ctx.Sessions() {
			settings, err := loadSettings(session.Store())
			if err != nil {
				return fmt.Errorf("failed to load CalDAV settings: %v", err)
			}
			settings.ShowCompleted = on
			if err := session.Store().Put(settingsKey, settings); err != nil {
				return fmt.Errorf("failed to save CalDAV settings: %v", err)
			}
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr("/tasks"))
	})

	GET("/calendar", func(ctx *alborz.Context) error {
		baseSettings, err := alborzbase.LoadSettings(ctx.Session.Store())
		if err != nil {
			return fmt.Errorf("failed to load settings: %v", err)
		}
		loc := alborzbase.UserLocation(ctx)

		var start time.Time
		if s := ctx.QueryParam("month"); s != "" {
			var err error
			start, err = time.Parse(monthPageLayout, s)
			if err != nil {
				return fmt.Errorf("failed to parse month: %v", err)
			}
			start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, loc)
		} else {
			now := time.Now().In(loc)
			start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		}
		firstDayOfWeek := baseSettings.FirstDayOfWeek

		view := ctx.QueryParam("view")
		if view != "" && view != "list" {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid view")
		}
		template := "calendar.html"
		if view == "list" {
			template = "calendar-list.html"
		}

		// Pad a week each way: the grid shows adjacent-month days, and a
		// fixed window keeps the cache key stable.
		monthEnd := start.AddDate(0, 1, 0)
		queryStart := start.AddDate(0, 0, -7)
		queryEnd := monthEnd.AddDate(0, 0, 7)

		// The agenda answers "what is next", so in the month you are
		// living in it starts at today; span=month asks for the whole of
		// it, the same range the grid draws.
		span := ctx.QueryParam("span")
		if span != "" && span != "month" {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid span")
		}
		since := start
		thisMonth := false
		if now := time.Now().In(loc); now.Year() == start.Year() && now.Month() == start.Month() {
			thisMonth = true
			if span != "month" {
				since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
			}
		}

		offset := (int(start.Weekday()) - firstDayOfWeek + 7) % 7
		gridStart := start.AddDate(0, 0, -offset)
		daysInMonth := time.Date(start.Year(), start.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		totalCells := offset + daysInMonth
		rows := (totalCells + 6) / 7

		accounts, err := p.pooledCalendars(ctx)
		if err != nil {
			return err
		}

		// Visibility is each account's own setting.
		var calendarInfos []CalendarInfo
		var sites []querySite
		for _, acc := range accounts {
			settings, err := loadSettings(acc.session.Store())
			if err != nil {
				return fmt.Errorf("failed to load CalDAV settings: %v", err)
			}
			visibleSet := make(map[string]bool)
			for _, path := range settings.VisibleCalendars {
				visibleSet[canonicalCollectionPath(path)] = true
			}
			for _, cal := range acc.calendars {
				if !cal.SupportsEvent() {
					continue
				}
				cal.Visible = !settings.CalendarFilter || visibleSet[cal.Path]
				calendarInfos = append(calendarInfos, cal)
				if cal.Visible {
					sites = append(sites, querySite{account: cal.Account, client: acc.client, path: cal.Path, name: cal.Name})
				}
			}
		}
		query := caldav.CalendarQuery{
			CompRequest: caldav.CalendarCompRequest{
				Name:  "VCALENDAR",
				Props: []string{"VERSION"},
				Comps: []caldav.CalendarCompRequest{{
					Name: "VEVENT",
					Props: []string{
						"SUMMARY",
						"UID",
						"DTSTART",
						"DTEND",
						"DURATION",
					},
				}},
				Expand: &caldav.CalendarExpandRequest{
					Start: queryStart,
					End:   queryEnd,
				},
			},
			CompFilter: caldav.CompFilter{
				Name: "VCALENDAR",
				Comps: []caldav.CompFilter{{
					Name:  "VEVENT",
					Start: queryStart,
					End:   queryEnd,
				}},
			},
		}

		events, err := querySites(ctx, sites, &query)
		if err != nil {
			return err
		}

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
		eventMap := make(map[time.Time][]CalendarObject)
		for _, ev := range events {
			co := ev.Data.Events()[0]
			startTime, _ := co.DateTimeStart(nil)
			endTime, _ := co.DateTimeEnd(nil)
			var first, last time.Time
			if ev.AllDay() {
				first = writtenDay(startTime)
				last = first
				if l := writtenDay(endTime).AddDate(0, 0, -1); l.After(last) {
					last = l
				}
			} else {
				first, last = day(startTime), day(startTime)
				if endTime.After(startTime) {
					if l := day(endTime.Add(-time.Nanosecond)); l.After(last) {
						last = l
					}
				}
			}
			if first.Before(gridStart) {
				first = gridStart
			}
			for d := first; !d.After(last) && d.Before(gridEnd); d = d.AddDate(0, 0, 1) {
				eventMap[d] = append(eventMap[d], ev)
			}
		}

		for _, evs := range eventMap {
			sort.Slice(evs, func(i, j int) bool {
				ti, _ := evs[i].Data.Events()[0].DateTimeStart(nil)
				tj, _ := evs[j].Data.Events()[0].DateTimeStart(nil)
				return ti.Before(tj)
			})
		}

		return ctx.Render(http.StatusOK, template, &CalendarRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).
				WithTitle(ctx.T("nav.calendar") + ": " + ctx.MonthYearIn(start)),
			Time:      start,
			Now:       time.Now().In(loc),
			Since:     since,
			Span:      span,
			ThisMonth: thisMonth,
			Calendars: calendarInfos,
			Dates:     dates,
			Events:    events,
			Page:      start.Format(monthPageLayout),
			View:      view,
			PrevPage:  start.AddDate(0, -1, 0).Format(monthPageLayout),
			NextPage:  start.AddDate(0, 1, 0).Format(monthPageLayout),
			PrevTime:  start.AddDate(0, -1, 0),
			NextTime:  start.AddDate(0, 1, 0),

			EventsForDate: func(when time.Time) []CalendarObject {
				if events, ok := eventMap[day(when)]; ok {
					return events
				}
				return nil
			},

			ColorForPath: func(account, eventPath string) string {
				for _, cal := range calendarInfos {
					if cal.Account == account && strings.HasPrefix(eventPath, cal.Path) {
						return cal.Color
					}
				}
				return ""
			},
			OwnerLabel: func(account, eventPath string) string {
				for _, cal := range calendarInfos {
					if cal.Account == account && strings.HasPrefix(eventPath, cal.Path) {
						if len(accounts) > 1 {
							return cal.Name + " — " + alborz.ShortAccount(account, ctx.Accounts())
						}
						return cal.Name
					}
				}
				return ""
			},

			Sub: func(a, b int) int {
				// Why isn't this built-in, come on Go
				return a - b
			},
		})
	})

	GET("/calendar/date", func(ctx *alborz.Context) error {
		loc := alborzbase.UserLocation(ctx)

		var start time.Time
		if s := ctx.QueryParam("date"); s != "" {
			var err error
			start, err = time.ParseInLocation(datePageLayout, s, loc)
			if err != nil {
				return fmt.Errorf("failed to parse date: %v", err)
			}
		} else {
			now := time.Now().In(loc)
			start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		}
		end := start.AddDate(0, 0, 1)

		accounts, err := p.pooledCalendars(ctx)
		if err != nil {
			return err
		}

		var calendarInfos []CalendarInfo
		var sites []querySite
		for _, acc := range accounts {
			settings, err := loadSettings(acc.session.Store())
			if err != nil {
				return fmt.Errorf("failed to load CalDAV settings: %v", err)
			}
			visibleSet := make(map[string]bool)
			for _, path := range settings.VisibleCalendars {
				visibleSet[canonicalCollectionPath(path)] = true
			}
			for _, cal := range acc.calendars {
				if !cal.SupportsEvent() {
					continue
				}
				// The aside lists every calendar with its checkbox state;
				// only the visible ones are queried.
				cal.Visible = !settings.CalendarFilter || visibleSet[cal.Path]
				calendarInfos = append(calendarInfos, cal)
				if cal.Visible {
					sites = append(sites, querySite{account: cal.Account, client: acc.client, path: cal.Path, name: cal.Name})
				}
			}
		}

		query := caldav.CalendarQuery{
			CompRequest: caldav.CalendarCompRequest{
				Name:  "VCALENDAR",
				Props: []string{"VERSION"},
				Comps: []caldav.CalendarCompRequest{{
					Name: "VEVENT",
					Props: []string{
						"SUMMARY",
						"UID",
						"DTSTART",
						"DTEND",
						"DURATION",
					},
				}},
				Expand: &caldav.CalendarExpandRequest{
					Start: start,
					End:   end,
				},
			},
			CompFilter: caldav.CompFilter{
				Name: "VCALENDAR",
				Comps: []caldav.CompFilter{{
					Name:  "VEVENT",
					Start: start,
					End:   end,
				}},
			},
		}

		events, err := querySites(ctx, sites, &query)
		if err != nil {
			return err
		}

		sort.Slice(events, func(i, j int) bool {
			ti, _ := events[i].Data.Events()[0].DateTimeStart(nil)
			tj, _ := events[j].Data.Events()[0].DateTimeStart(nil)
			return ti.Before(tj)
		})

		return ctx.Render(http.StatusOK, "calendar-date.html", &CalendarDateRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).
				WithTitle(ctx.T("nav.calendar") + ": " + ctx.MonthYearIn(start) + start.Format(", 2")),
			Time:      start,
			Calendars: calendarInfos,
			Events:    events,
			PrevPage:  start.AddDate(0, 0, -1).Format(datePageLayout),
			NextPage:  start.AddDate(0, 0, 1).Format(datePageLayout),
			ColorForPath: func(account, eventPath string) string {
				for _, cal := range calendarInfos {
					if cal.Account == account && strings.HasPrefix(eventPath, cal.Path) {
						return cal.Color
					}
				}
				return ""
			},
			OwnerLabel: func(account, eventPath string) string {
				for _, cal := range calendarInfos {
					if cal.Account == account && strings.HasPrefix(eventPath, cal.Path) {
						if len(accounts) > 1 {
							return cal.Name + " — " + alborz.ShortAccount(account, ctx.Accounts())
						}
						return cal.Name
					}
				}
				return ""
			},
		})
	})

	GET("/calendar/:path", func(ctx *alborz.Context) error {
		path, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		c, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		// The object's path starts with its calendar's.
		calendar := &calendars[0]
		for i := range calendars {
			if strings.HasPrefix(path, calendars[i].Path) {
				calendar = &calendars[i]
				break
			}
		}

		multiGet := caldav.CalendarMultiGet{
			CompRequest: caldav.CalendarCompRequest{
				Name:  "VCALENDAR",
				Props: []string{"VERSION"},
				Comps: []caldav.CalendarCompRequest{{
					Name: "VEVENT",
					Props: []string{
						"SUMMARY",
						"DESCRIPTION",
						"UID",
						"DTSTART",
						"DTEND",
						"DURATION",
					},
				}},
			},
		}

		events, err := c.MultiGetCalendar(ctx.Request().Context(), path, &multiGet)
		if err != nil {
			return fmt.Errorf("failed to multi-get calendar: %v", err)
		}
		if len(events) != 1 {
			return fmt.Errorf("expected exactly one calendar object with path %q, got %v", path, len(events))
		}
		event := &events[0]
		summary, _ := event.Data.Events()[0].Props.Text("SUMMARY")

		return ctx.Render(http.StatusOK, "event.html", &EventRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(summary),
			Calendar:       calendar,
			Event:          CalendarObject{CalendarObject: event},
		})
	})

	updateEvent := func(ctx *alborz.Context) error {
		calendarObjectPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		loc := alborzbase.UserLocation(ctx)

		var c *caldav.Client
		var calendars []CalendarInfo
		var groups []CalendarGroup
		var co *caldav.CalendarObject
		var event *ical.Event
		var currentCalendar *CalendarInfo
		if calendarObjectPath != "" {
			c, calendars, err = p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return err
			}
			co, err = getCalendarObject(ctx, c, calendarObjectPath)
			if err != nil {
				return fmt.Errorf("failed to get CalDAV event: %v", err)
			}
			events := co.Data.Events()
			if len(events) != 1 {
				return fmt.Errorf("expected exactly one event, got %d", len(events))
			}
			event = &events[0]
			for i := range calendars {
				if strings.HasPrefix(co.Path, calendars[i].Path) {
					currentCalendar = &calendars[i]
					break
				}
			}
		} else {
			// Creating is pooled: it must not fail merely because the active
			// account has no CalDAV collection of this kind.
			groups, err = p.writableGroups(ctx, CalendarInfo.SupportsEvent)
			if err != nil {
				return err
			}
			if len(groups) == 0 || len(groups[0].Calendars) == 0 {
				return fmt.Errorf("no writable calendars")
			}
			event = ical.NewEvent()
			currentCalendar = &groups[0].Calendars[0]
		}

		if ctx.Request().Method == "POST" {
			summary := ctx.FormValue("summary")
			description := ctx.FormValue("description")
			calendarPath := ctx.FormValue("calendar")

			// The form answers its own invalid input: the same page with
			// an alert, never a status page the browser writes.
			reject := func(message string) error {
				return ctx.Render(http.StatusUnprocessableEntity, "update-event.html", &UpdateEventRenderData{
					BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("calendar.createtitle")),
					AccountCals:    groups,
					Calendar:       currentCalendar,
					CalendarObject: co,
					Event:          event,
					AllDay:         ctx.FormValue("allday") != "",
					StartTime:      ctx.FormValue("start"),
					EndTime:        ctx.FormValue("end"),
					StartDate:      ctx.FormValue("start_date"),
					EndDate:        ctx.FormValue("end_date"),
					Error:          message,
				})
			}
			if summary == "" {
				return reject(ctx.T("form.summaryneeded"))
			}

			saveClient := c
			var createAcct string
			if co == nil {
				// The form's choice names its owner as "account|path".
				saveClient, calendarPath, createAcct, err = p.resolveCreateCalendar(ctx, calendarPath, CalendarInfo.SupportsEvent)
				if err != nil {
					return reject(ctx.T("form.destinationneeded"))
				}
			}

			// An event that occupies whole days has no time of day to
			// ask for, and asking anyway is what made a two-day
			// conference impossible to enter.
			allDay := ctx.FormValue("allday") != ""
			var start, end time.Time
			if allDay {
				start, err = parseDate(ctx.FormValue("start_date"), loc)
				if err != nil {
					return reject(ctx.T("form.datesneeded"))
				}
				end, err = parseDate(ctx.FormValue("end_date"), loc)
				if err != nil {
					end = start
				}
				if start.After(end) {
					return reject(ctx.T("form.endbeforestart"))
				}
				// The form asks for the last day; iCalendar 3.8.2.2
				// wants the day after it, since DTEND is exclusive.
				end = end.AddDate(0, 0, 1)
			} else {
				start, err = parseDateTime(ctx.FormValue("start"), loc)
				if err != nil {
					return reject(ctx.T("form.datesneeded"))
				}
				end, err = parseDateTime(ctx.FormValue("end"), loc)
				if err != nil {
					return reject(ctx.T("form.datesneeded"))
				}
				if start.After(end) {
					return reject(ctx.T("form.endbeforestart"))
				}
				if start == end {
					end = start.Add(24 * time.Hour)
				}
			}

			event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
			event.Props.SetText(ical.PropSummary, summary)
			if allDay {
				event.Props.SetDate(ical.PropDateTimeStart, start)
				event.Props.SetDate(ical.PropDateTimeEnd, end)
			} else {
				event.Props.SetDateTime(ical.PropDateTimeStart, start)
				event.Props.SetDateTime(ical.PropDateTimeEnd, end)
			}
			event.Props.Del(ical.PropDuration)

			if description != "" {
				description = strings.ReplaceAll(description, "\r", "")
				event.Props.SetText(ical.PropDescription, description)
			} else {
				event.Props.Del(ical.PropDescription)
			}

			newID := uuid.New()
			if prop := event.Props.Get(ical.PropUID); prop == nil {
				event.Props.SetText(ical.PropUID, newID.String())
			}

			var cal *ical.Calendar
			var savePath string
			if co != nil {
				cal = co.Data
				savePath = co.Path
			} else {
				cal = ical.NewCalendar()
				cal.Props.SetText(ical.PropProductID, "-//mehdix.org//alborz//EN")
				cal.Props.SetText(ical.PropVersion, "2.0")
				cal.Children = append(cal.Children, event.Component)
				savePath = path.Join(calendarPath, newID.String()+".ics")
			}
			co, err = saveClient.PutCalendarObject(ctx.Request().Context(), savePath, cal)
			if err != nil {
				return fmt.Errorf("failed to put calendar object: %v", err)
			}

			if createAcct != "" {
				return ctx.Redirect(http.StatusFound, CalendarObject{CalendarObject: co}.URL()+"?account="+alborz.AccountParam(createAcct))
			}
			return ctx.Redirect(http.StatusFound, ctx.AccountPath(CalendarObject{CalendarObject: co}.URL()))
		}

		summary, _ := event.Props.Text("SUMMARY")

		data := &UpdateEventRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("title.update"), summary)),
			AccountCals:    groups,
			Calendar:       currentCalendar,
			CalendarObject: co,
			Event:          event,
		}
		fillEventForm(data, loc)
		return ctx.Render(http.StatusOK, "update-event.html", data)
	}

	// The object exactly as the server stores it. Nothing here parses
	// it: a raw view is only useful while it is verbatim.
	rawObject := func(ctx *alborz.Context) error {
		path, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}
		body, err := c.Open(ctx.Request().Context(), path)
		if err != nil {
			return err
		}
		defer body.Close()
		return ctx.Stream(http.StatusOK, "text/plain; charset=utf-8", body)
	}

	GET("/calendar/:path/raw", rawObject)
	GET("/tasks/:path/raw", rawObject)

	GET("/calendars/create", handleCreateCalendar(p))
	POST("/calendars/create", handleCreateCalendar(p))

	GET("/calendar/create", updateEvent)
	POST("/calendar/create", updateEvent)

	GET("/calendar/:path/update", updateEvent)
	POST("/calendar/:path/update", updateEvent)

	POST("/calendar/:path/delete", func(ctx *alborz.Context) error {
		objPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		if err := c.RemoveAll(ctx.Request().Context(), objPath); err != nil {
			return fmt.Errorf("failed to delete calendar object: %v", err)
		}

		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/calendar"))
	})

	// Tasks routes
	GET("/tasks", func(ctx *alborz.Context) error {
		loc := alborzbase.UserLocation(ctx)
		accounts, err := p.pooledCalendars(ctx)
		if err != nil {
			return err
		}

		type taskSite struct {
			cal           CalendarInfo
			client        *caldav.Client
			showCompleted bool
		}
		showCompleted := true
		var calendarInfos []CalendarInfo
		var sites []taskSite
		for _, acc := range accounts {
			settings, err := loadSettings(acc.session.Store())
			if err != nil {
				return fmt.Errorf("failed to load CalDAV settings: %v", err)
			}
			if !settings.ShowCompleted {
				showCompleted = false
			}
			visibleSet := make(map[string]bool)
			for _, path := range settings.VisibleTasks {
				visibleSet[canonicalCollectionPath(path)] = true
			}
			for _, cal := range acc.calendars {
				if !cal.SupportsTodo() {
					continue
				}
				cal.Visible = !settings.TaskFilter || visibleSet[cal.Path]
				calendarInfos = append(calendarInfos, cal)
				if cal.Visible {
					sites = append(sites, taskSite{cal: cal, client: acc.client, showCompleted: settings.ShowCompleted})
				}
			}
		}

		search := ctx.QueryParam("query")
		sortKey := ctx.QueryParam("sort")
		sortDir := ctx.QueryParam("dir")
		if sortKey == "" {
			sortKey = "summary"
		}
		if sortKey != "status" && sortKey != "summary" && sortKey != "account" && sortKey != "calendar" && sortKey != "due" {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid task sort")
		}
		if sortDir != "" && sortDir != "asc" && sortDir != "desc" {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid task sort direction")
		}

		query := caldav.CalendarQuery{
			CompRequest: caldav.CalendarCompRequest{
				Name:  "VCALENDAR",
				Props: []string{"VERSION"},
				Comps: []caldav.CalendarCompRequest{{
					Name: "VTODO",
					Props: []string{
						"SUMMARY",
						"UID",
						"DUE",
						"STATUS",
						"DESCRIPTION",
					},
				}},
			},
			CompFilter: caldav.CompFilter{
				Name:  "VCALENDAR",
				Comps: []caldav.CompFilter{{Name: "VTODO"}},
			},
		}

		type taskQueryResult struct {
			site  taskSite
			tasks []caldav.CalendarObject
			err   error
		}

		reqCtx := ctx.Request().Context()
		results := make(chan taskQueryResult, len(sites))
		sem := make(chan struct{}, maxCalendarQueryConcurrency)
		for _, site := range sites {
			go func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				calTasks, err := site.client.QueryCalendar(reqCtx, site.cal.Path, &query)
				results <- taskQueryResult{site: site, tasks: calTasks, err: err}
			}()
		}

		var taskRows []TaskRow
		for range sites {
			result := <-results
			if result.err != nil {
				return fmt.Errorf("failed to query tasks from %s: %v", result.site.cal.Name, result.err)
			}

			for _, task := range result.tasks {
				todo := getFirstTodo(task.Data)
				if todo == nil {
					continue
				}
				status, _ := todo.Props.Text("STATUS")
				if status == "COMPLETED" && !result.site.showCompleted {
					continue
				}
				if search != "" {
					summary, _ := todo.Props.Text("SUMMARY")
					description, _ := todo.Props.Text("DESCRIPTION")
					haystack := strings.ToLower(summary + "\n" + description)
					if !strings.Contains(haystack, strings.ToLower(search)) {
						continue
					}
				}
				summary, _ := todo.Props.Text("SUMMARY")
				// The raw property value is an iCal timestamp
				// ("20260830T100000Z"), which is not a thing to show
				// anyone; parse it and let the page write the date.
				due, _ := todo.Props.DateTime("DUE", loc)
				taskRows = append(taskRows, TaskRow{
					Task:      TaskObject{CalendarObject: &task, Account: result.site.cal.Account},
					Calendar:  result.site.cal,
					Summary:   summary,
					Status:    status,
					Due:       due,
					Completed: status == "COMPLETED",
				})
			}
		}

		value := func(row TaskRow) string {
			switch sortKey {
			case "status":
				if row.Completed {
					return "1"
				}
				return "0"
			case "account":
				return strings.ToLower(row.Task.Account)
			case "calendar":
				return strings.ToLower(row.Calendar.Name)
			case "due":
				// Undated tasks belong after dated ones in ascending order.
				if row.Due.IsZero() {
					return "\uffff"
				}
				return row.Due.Format(time.RFC3339)
			default:
				return strings.ToLower(row.Summary)
			}
		}
		sort.SliceStable(taskRows, func(i, j int) bool {
			a, b := value(taskRows[i]), value(taskRows[j])
			if a == b {
				a = strings.ToLower(taskRows[i].Task.Account + "\x00" + taskRows[i].Calendar.Name + "\x00" + taskRows[i].Summary)
				b = strings.ToLower(taskRows[j].Task.Account + "\x00" + taskRows[j].Calendar.Name + "\x00" + taskRows[j].Summary)
			}
			if sortDir == "desc" {
				return a > b
			}
			return a < b
		})

		return ctx.Render(http.StatusOK, "tasks.html", &TasksRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("title.tasks")),
			Calendars:      calendarInfos,
			Tasks:          taskRows,
			ShowCompleted:  showCompleted,
			Query:          search,
			Sort:           sortKey,
			SortDir:        sortDir,
		})
	})

	GET("/tasks/:path", func(ctx *alborz.Context) error {
		path, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		c, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		// The object's path starts with its calendar's.
		calendar := &calendars[0]
		for i := range calendars {
			if strings.HasPrefix(path, calendars[i].Path) {
				calendar = &calendars[i]
				break
			}
		}

		multiGet := caldav.CalendarMultiGet{
			CompRequest: caldav.CalendarCompRequest{
				Name:  "VCALENDAR",
				Props: []string{"VERSION"},
				Comps: []caldav.CalendarCompRequest{{
					Name: "VTODO",
					Props: []string{
						"SUMMARY",
						"DESCRIPTION",
						"UID",
						"DUE",
						"STATUS",
					},
				}},
			},
		}

		tasks, err := c.MultiGetCalendar(ctx.Request().Context(), path, &multiGet)
		if err != nil {
			return fmt.Errorf("failed to get task: %v", err)
		}
		if len(tasks) != 1 {
			return fmt.Errorf("expected exactly one task with path %q, got %v", path, len(tasks))
		}
		task := &tasks[0]
		todo := getFirstTodo(task.Data)
		if todo == nil {
			return fmt.Errorf("no VTODO component found")
		}
		summary, _ := todo.Props.Text("SUMMARY")

		return ctx.Render(http.StatusOK, "task.html", &TaskRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(summary),
			Calendar:       calendar,
			Task:           TaskObject{CalendarObject: task},
		})
	})

	updateTask := func(ctx *alborz.Context) error {
		taskPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		loc := alborzbase.UserLocation(ctx)

		var c *caldav.Client
		var calendars []CalendarInfo
		var groups []CalendarGroup
		var co *caldav.CalendarObject
		var todo *ical.Component
		var currentCalendar *CalendarInfo
		if taskPath != "" {
			c, calendars, err = p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return err
			}
			co, err = getCalendarObject(ctx, c, taskPath)
			if err != nil {
				return fmt.Errorf("failed to get task: %v", err)
			}
			todo = getFirstTodo(co.Data)
			if todo == nil {
				return fmt.Errorf("no VTODO component found")
			}
			for i := range calendars {
				if strings.HasPrefix(co.Path, calendars[i].Path) {
					currentCalendar = &calendars[i]
					break
				}
			}
		} else {
			groups, err = p.writableGroups(ctx, CalendarInfo.SupportsTodo)
			if err != nil {
				return err
			}
			if len(groups) == 0 || len(groups[0].Calendars) == 0 {
				return fmt.Errorf("no writable calendars")
			}
			todo = ical.NewComponent(ical.CompToDo)
			currentCalendar = &groups[0].Calendars[0]
		}

		if ctx.Request().Method == "POST" {
			summary := ctx.FormValue("summary")
			description := ctx.FormValue("description")
			dueDate := ctx.FormValue("due-date")
			calendarPath := ctx.FormValue("calendar")

			reject := func(message string) error {
				return ctx.Render(http.StatusUnprocessableEntity, "update-task.html", &UpdateTaskRenderData{
					BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("tasks.createtitle")),
					AccountCals:    groups,
					Calendar:       currentCalendar,
					CalendarObject: co,
					Todo:           todo,
					Error:          message,
				})
			}
			if summary == "" {
				return reject(ctx.T("form.summaryneeded"))
			}

			saveClient := c
			var createAcct string
			if co == nil {
				// The form's choice names its owner as "account|path".
				saveClient, calendarPath, createAcct, err = p.resolveCreateCalendar(ctx, calendarPath, CalendarInfo.SupportsTodo)
				if err != nil {
					return reject(ctx.T("form.destinationneeded"))
				}
			}

			todo.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
			todo.Props.SetText(ical.PropSummary, summary)

			if description != "" {
				description = strings.ReplaceAll(description, "\r", "")
				todo.Props.SetText(ical.PropDescription, description)
			} else {
				todo.Props.Del(ical.PropDescription)
			}

			if dueDate != "" {
				due, err := time.ParseInLocation(inputDateLayout, dueDate, loc)
				if err != nil {
					return reject(ctx.T("form.duedate"))
				}
				todo.Props.SetDateTime(ical.PropDue, due)
			} else {
				todo.Props.Del(ical.PropDue)
			}

			newID := uuid.New()
			if prop := todo.Props.Get(ical.PropUID); prop == nil {
				todo.Props.SetText(ical.PropUID, newID.String())
				todo.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
			}

			var cal *ical.Calendar
			var savePath string
			if co != nil {
				cal = co.Data
				savePath = co.Path
			} else {
				cal = ical.NewCalendar()
				cal.Props.SetText(ical.PropProductID, "-//mehdix.org//alborz//EN")
				cal.Props.SetText(ical.PropVersion, "2.0")
				cal.Children = append(cal.Children, todo)
				savePath = path.Join(calendarPath, newID.String()+".ics")
			}
			co, err = saveClient.PutCalendarObject(ctx.Request().Context(), savePath, cal)
			if err != nil {
				return fmt.Errorf("failed to save task: %v", err)
			}

			if createAcct != "" {
				return ctx.Redirect(http.StatusFound, TaskObject{CalendarObject: co}.URL()+"?account="+alborz.AccountParam(createAcct))
			}
			return ctx.Redirect(http.StatusFound, ctx.AccountPath(TaskObject{CalendarObject: co}.URL()))
		}

		summary, _ := todo.Props.Text("SUMMARY")

		return ctx.Render(http.StatusOK, "update-task.html", &UpdateTaskRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("title.update"), summary)),
			AccountCals:    groups,
			Calendar:       currentCalendar,
			CalendarObject: co,
			Todo:           todo,
		})
	}

	GET("/tasks/create", updateTask)
	POST("/tasks/create", updateTask)

	GET("/tasks/:path/edit", updateTask)
	POST("/tasks/:path/edit", updateTask)

	POST("/tasks/:path/delete", func(ctx *alborz.Context) error {
		objPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		if err := c.RemoveAll(ctx.Request().Context(), objPath); err != nil {
			return fmt.Errorf("failed to delete task: %v", err)
		}

		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/tasks"))
	})

	POST("/tasks/:path/complete", func(ctx *alborz.Context) error {
		taskPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		co, err := getCalendarObject(ctx, c, taskPath)
		if err != nil {
			return fmt.Errorf("failed to get task: %v", err)
		}

		todo := getFirstTodo(co.Data)
		if todo == nil {
			return fmt.Errorf("no VTODO component found")
		}

		status, _ := todo.Props.Text("STATUS")
		if status == "COMPLETED" {
			todo.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
			todo.Props.Del(ical.PropCompleted)
		} else {
			todo.Props.SetText(ical.PropStatus, "COMPLETED")
			todo.Props.SetDateTime(ical.PropCompleted, time.Now().UTC())
		}
		todo.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())

		_, err = c.PutCalendarObject(ctx.Request().Context(), co.Path, co.Data)
		if err != nil {
			return fmt.Errorf("failed to update task: %v", err)
		}

		// A completion from the merged list returns to it; next comes
		// from the page's own form, so it must stay a local path.
		if next := ctx.FormValue("next"); strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
			return ctx.Redirect(http.StatusFound, next)
		}
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/tasks"))
	})
}
