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
	"unicode"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type CalendarRenderData struct {
	alborz.BaseRenderData
	Time               time.Time
	Now                time.Time
	Dates              []time.Time
	Calendars          []CalendarInfo
	Events             []CalendarObject
	Page               string // the shown month as a ?month= value
	View               string // "" for the month grid, "list" for the agenda
	PrevPage, NextPage string
	PrevTime, NextTime time.Time

	EventsForDate func(time.Time) []CalendarObject
	ColorForPath  func(string) string
	DaySuffix     func(n int) string
	Sub           func(a, b int) int
}

type CalendarDateRenderData struct {
	alborz.BaseRenderData
	Time               time.Time
	Events             []CalendarObject
	PrevPage, NextPage string

	ColorForPath func(string) string
}

type EventRenderData struct {
	alborz.BaseRenderData
	Calendar *CalendarInfo
	Event    CalendarObject
}

type UpdateEventRenderData struct {
	alborz.BaseRenderData
	Calendars      []CalendarInfo
	Calendar       *CalendarInfo
	CalendarObject *caldav.CalendarObject // nil if creating a new event
	Event          *ical.Event
}

type Settings struct {
	CalendarFilter   bool
	VisibleCalendars []string
	TaskFilter       bool
	VisibleTasks     []string
	ShowCompleted    bool
}

type TaskGroup struct {
	Calendar CalendarInfo
	Tasks    []TaskObject
}

type TasksRenderData struct {
	alborz.BaseRenderData
	Calendars     []CalendarInfo
	TaskGroups    []TaskGroup
	ShowCompleted bool
	Query         string
}

type TaskRenderData struct {
	alborz.BaseRenderData
	Calendar *CalendarInfo
	Task     TaskObject
}

type UpdateTaskRenderData struct {
	alborz.BaseRenderData
	Calendars      []CalendarInfo
	Calendar       *CalendarInfo
	CalendarObject *caldav.CalendarObject
	Todo           *ical.Component
}

const (
	monthPageLayout             = "2006-01"
	datePageLayout              = "2006-01-02"
	maxCalendarQueryConcurrency = 4
	settingsKey                 = "caldav.settings"
)

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

func detectScript(s string) string {
	for _, r := range s {
		if unicode.IsLetter(r) {
			switch {
			case unicode.Is(unicode.Arabic, r):
				return "arabic"
			case unicode.Is(unicode.Hebrew, r):
				return "hebrew"
			case unicode.Is(unicode.Cyrillic, r):
				return "cyrillic"
			case unicode.Is(unicode.Han, r):
				return "han"
			case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
				return "japanese"
			case unicode.Is(unicode.Hangul, r):
				return "hangul"
			case unicode.Is(unicode.Greek, r):
				return "greek"
			case unicode.Is(unicode.Latin, r):
				return "latin"
			default:
				return "other"
			}
		}
	}
	return "other"
}

func sortTasksByScript(tasks []TaskObject) {
	sort.SliceStable(tasks, func(i, j int) bool {
		todoI := getFirstTodo(tasks[i].Data)
		todoJ := getFirstTodo(tasks[j].Data)
		if todoI == nil || todoJ == nil {
			return false
		}
		summaryI, _ := todoI.Props.Text("SUMMARY")
		summaryJ, _ := todoJ.Props.Text("SUMMARY")
		return detectScript(summaryI) < detectScript(summaryJ)
	})
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
		return ctx.Redirect(http.StatusFound, ctx.Request().URL.RequestURI())
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
		settings.ShowCompleted = params.Get("show-completed") == "1"
		if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
			return fmt.Errorf("failed to save CalDAV settings: %v", err)
		}
		return ctx.Redirect(http.StatusFound, ctx.Request().URL.RequestURI())
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

		offset := (int(start.Weekday()) - firstDayOfWeek + 7) % 7
		gridStart := start.AddDate(0, 0, -offset)
		daysInMonth := time.Date(start.Year(), start.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		totalCells := offset + daysInMonth
		rows := (totalCells + 6) / 7

		c, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		settings, err := loadSettings(ctx.Session.Store())
		if err != nil {
			return fmt.Errorf("failed to load CalDAV settings: %v", err)
		}

		calendarFilter := settings.CalendarFilter
		visibleSet := make(map[string]bool)
		for _, path := range settings.VisibleCalendars {
			visibleSet[canonicalCollectionPath(path)] = true
		}

		var calendarInfos []CalendarInfo
		for _, cal := range calendars {
			if !cal.SupportsEvent() {
				continue
			}
			visible := !calendarFilter || visibleSet[cal.Path]
			calendarInfos = append(calendarInfos, CalendarInfo{
				Path:                  cal.Path,
				Name:                  cal.Name,
				Color:                 cal.Color,
				Visible:               visible,
				SupportedComponentSet: cal.SupportedComponentSet,
			})
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

		type calendarQueryResult struct {
			name   string
			events []caldav.CalendarObject
			err    error
		}

		visibleCalendars := make([]CalendarInfo, 0, len(calendarInfos))
		for _, calInfo := range calendarInfos {
			if calInfo.Visible {
				visibleCalendars = append(visibleCalendars, calInfo)
			}
		}

		reqCtx := ctx.Request().Context()
		results := make(chan calendarQueryResult, len(visibleCalendars))
		sem := make(chan struct{}, maxCalendarQueryConcurrency)
		for _, calInfo := range visibleCalendars {
			go func() {
				sem <- struct{}{}
				defer func() { <-sem }()

				calEvents, err := c.QueryCalendar(reqCtx, calInfo.Path, &query)
				results <- calendarQueryResult{
					name:   calInfo.Name,
					events: calEvents,
					err:    err,
				}
			}()
		}

		var events []caldav.CalendarObject
		for i := 0; i < len(visibleCalendars); i++ {
			result := <-results
			if result.err != nil {
				return fmt.Errorf("failed to query calendar %s: %v", result.name, result.err)
			}
			events = append(events, result.events...)
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
		eventMap := make(map[time.Time][]CalendarObject)
		for _, ev := range events {
			ev := ev // make a copy
			// TODO: include event on each date for which it is active
			co := ev.Data.Events()[0]
			startTime, _ := co.DateTimeStart(nil)
			key := day(startTime)
			eventMap[key] = append(eventMap[key], CalendarObject{&ev})
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
				WithTitle("Calendar: " + start.Format("January 2006")),
			Time:      start,
			Now:       time.Now().In(loc),
			Calendars: calendarInfos,
			Dates:     dates,
			Events:    newCalendarObjectList(events),
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

			ColorForPath: func(eventPath string) string {
				for _, cal := range calendarInfos {
					if strings.HasPrefix(eventPath, cal.Path) {
						return cal.Color
					}
				}
				return ""
			},

			DaySuffix: func(n int) string {
				if n%100 >= 11 && n%100 <= 13 {
					return "th"
				}
				return map[int]string{
					0: "th",
					1: "st",
					2: "nd",
					3: "rd",
					4: "th",
					5: "th",
					6: "th",
					7: "th",
					8: "th",
					9: "th",
				}[n%10]
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

		c, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		settings, err := loadSettings(ctx.Session.Store())
		if err != nil {
			return fmt.Errorf("failed to load CalDAV settings: %v", err)
		}

		visibleSet := make(map[string]bool)
		for _, path := range settings.VisibleCalendars {
			visibleSet[canonicalCollectionPath(path)] = true
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

		type dateQueryResult struct {
			name   string
			events []caldav.CalendarObject
			err    error
		}

		var visibleCalendars []CalendarInfo
		for _, cal := range calendars {
			if !cal.SupportsEvent() {
				continue
			}
			if settings.CalendarFilter && !visibleSet[cal.Path] {
				continue
			}
			visibleCalendars = append(visibleCalendars, cal)
		}

		reqCtx := ctx.Request().Context()
		results := make(chan dateQueryResult, len(visibleCalendars))
		sem := make(chan struct{}, maxCalendarQueryConcurrency)
		for _, cal := range visibleCalendars {
			go func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				calEvents, err := c.QueryCalendar(reqCtx, cal.Path, &query)
				results <- dateQueryResult{name: cal.Name, events: calEvents, err: err}
			}()
		}

		var events []caldav.CalendarObject
		for i := 0; i < len(visibleCalendars); i++ {
			result := <-results
			if result.err != nil {
				return fmt.Errorf("failed to query calendar %s: %v", result.name, result.err)
			}
			events = append(events, result.events...)
		}

		sort.Slice(events, func(i, j int) bool {
			ti, _ := events[i].Data.Events()[0].DateTimeStart(nil)
			tj, _ := events[j].Data.Events()[0].DateTimeStart(nil)
			return ti.Before(tj)
		})

		return ctx.Render(http.StatusOK, "calendar-date.html", &CalendarDateRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).
				WithTitle("Calendar: " + start.Format("January 02, 2006")),
			Time:     start,
			Events:   newCalendarObjectList(events),
			PrevPage: start.AddDate(0, 0, -1).Format(datePageLayout),
			NextPage: start.AddDate(0, 0, 1).Format(datePageLayout),
			ColorForPath: func(eventPath string) string {
				for _, cal := range calendars {
					if strings.HasPrefix(eventPath, cal.Path) {
						return cal.Color
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
			Event:          CalendarObject{event},
		})
	})

	updateEvent := func(ctx *alborz.Context) error {
		calendarObjectPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		loc := alborzbase.UserLocation(ctx)

		c, allCalendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		var calendars []CalendarInfo
		for _, cal := range allCalendars {
			if cal.SupportsEvent() {
				calendars = append(calendars, cal)
			}
		}
		if len(calendars) == 0 {
			return fmt.Errorf("no calendars support events")
		}
		writable := make([]CalendarInfo, 0, len(calendars))
		for _, cal := range calendars {
			if cal.Writable {
				writable = append(writable, cal)
			}
		}

		var co *caldav.CalendarObject
		var event *ical.Event
		var currentCalendar *CalendarInfo
		if calendarObjectPath != "" {
			co, err = c.GetCalendarObject(ctx.Request().Context(), calendarObjectPath)
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
			if len(writable) == 0 {
				return fmt.Errorf("no writable calendars")
			}
			event = ical.NewEvent()
			currentCalendar = &writable[0]
		}

		if ctx.Request().Method == "POST" {
			summary := ctx.FormValue("summary")
			description := ctx.FormValue("description")
			calendarPath := ctx.FormValue("calendar")
			if co == nil && calendarByPath(writable, calendarPath) == nil {
				return echo.NewHTTPError(http.StatusBadRequest, "unknown calendar")
			}

			// TODO: whole-day events
			start, err := parseDateTime(ctx.FormValue("start"), loc)
			if err != nil {
				return err
			}
			end, err := parseDateTime(ctx.FormValue("end"), loc)
			if err != nil {
				return err
			}
			if start.After(end) {
				return echo.NewHTTPError(http.StatusBadRequest, "event start is after its end")
			}

			if start == end {
				end = start.Add(24 * time.Hour)
			}

			event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
			event.Props.SetText(ical.PropSummary, summary)
			event.Props.SetDateTime(ical.PropDateTimeStart, start)
			event.Props.SetDateTime(ical.PropDateTimeEnd, end)
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
			co, err = c.PutCalendarObject(ctx.Request().Context(), savePath, cal)
			if err != nil {
				return fmt.Errorf("failed to put calendar object: %v", err)
			}

			return ctx.Redirect(http.StatusFound, CalendarObject{co}.URL())
		}

		summary, _ := event.Props.Text("SUMMARY")

		return ctx.Render(http.StatusOK, "update-event.html", &UpdateEventRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("title.update"), summary)),
			Calendars:      writable,
			Calendar:       currentCalendar,
			CalendarObject: co,
			Event:          event,
		})
	}

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

		return ctx.Redirect(http.StatusFound, "/calendar")
	})

	// Tasks routes
	GET("/tasks", func(ctx *alborz.Context) error {
		c, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		settings, err := loadSettings(ctx.Session.Store())
		if err != nil {
			return fmt.Errorf("failed to load CalDAV settings: %v", err)
		}

		taskFilter := settings.TaskFilter
		visibleSet := make(map[string]bool)
		for _, path := range settings.VisibleTasks {
			visibleSet[canonicalCollectionPath(path)] = true
		}

		var calendarInfos []CalendarInfo
		for _, cal := range calendars {
			if !cal.SupportsTodo() {
				continue
			}
			visible := !taskFilter || visibleSet[cal.Path]
			calendarInfos = append(calendarInfos, CalendarInfo{
				Path:                  cal.Path,
				Name:                  cal.Name,
				Color:                 cal.Color,
				Visible:               visible,
				SupportedComponentSet: cal.SupportedComponentSet,
			})
		}

		showCompleted := settings.ShowCompleted
		search := ctx.QueryParam("query")

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
			cal   CalendarInfo
			tasks []caldav.CalendarObject
			err   error
		}

		var visibleCalendars []CalendarInfo
		for _, cal := range calendarInfos {
			if cal.Visible {
				visibleCalendars = append(visibleCalendars, cal)
			}
		}

		reqCtx := ctx.Request().Context()
		results := make(chan taskQueryResult, len(visibleCalendars))
		sem := make(chan struct{}, maxCalendarQueryConcurrency)
		for _, cal := range visibleCalendars {
			go func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				calTasks, err := c.QueryCalendar(reqCtx, cal.Path, &query)
				results <- taskQueryResult{cal: cal, tasks: calTasks, err: err}
			}()
		}

		var taskGroups []TaskGroup
		for i := 0; i < len(visibleCalendars); i++ {
			result := <-results
			if result.err != nil {
				return fmt.Errorf("failed to query tasks from %s: %v", result.cal.Name, result.err)
			}

			var filtered []caldav.CalendarObject
			for _, task := range result.tasks {
				todo := getFirstTodo(task.Data)
				if todo == nil {
					continue
				}
				status, _ := todo.Props.Text("STATUS")
				if status == "COMPLETED" && !showCompleted {
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
				filtered = append(filtered, task)
			}

			if len(filtered) > 0 {
				tasks := newTaskObjectList(filtered)
				sortTasksByScript(tasks)
				taskGroups = append(taskGroups, TaskGroup{
					Calendar: result.cal,
					Tasks:    tasks,
				})
			}
		}

		sort.Slice(taskGroups, func(i, j int) bool {
			return strings.ToLower(taskGroups[i].Calendar.Name) < strings.ToLower(taskGroups[j].Calendar.Name)
		})

		return ctx.Render(http.StatusOK, "tasks.html", &TasksRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("title.tasks")),
			Calendars:      calendarInfos,
			TaskGroups:     taskGroups,
			ShowCompleted:  showCompleted,
			Query:          search,
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
			Task:           TaskObject{task},
		})
	})

	updateTask := func(ctx *alborz.Context) error {
		taskPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		loc := alborzbase.UserLocation(ctx)

		c, allCalendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		var calendars []CalendarInfo
		for _, cal := range allCalendars {
			if cal.SupportsTodo() {
				calendars = append(calendars, cal)
			}
		}
		if len(calendars) == 0 {
			return fmt.Errorf("no calendars support tasks")
		}
		writable := make([]CalendarInfo, 0, len(calendars))
		for _, cal := range calendars {
			if cal.Writable {
				writable = append(writable, cal)
			}
		}

		var co *caldav.CalendarObject
		var todo *ical.Component
		var currentCalendar *CalendarInfo
		if taskPath != "" {
			co, err = c.GetCalendarObject(ctx.Request().Context(), taskPath)
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
			if len(writable) == 0 {
				return fmt.Errorf("no writable calendars")
			}
			todo = ical.NewComponent(ical.CompToDo)
			currentCalendar = &writable[0]
		}

		if ctx.Request().Method == "POST" {
			summary := ctx.FormValue("summary")
			description := ctx.FormValue("description")
			dueDate := ctx.FormValue("due-date")
			calendarPath := ctx.FormValue("calendar")
			if co == nil && calendarByPath(writable, calendarPath) == nil {
				return echo.NewHTTPError(http.StatusBadRequest, "unknown calendar")
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
					return echo.NewHTTPError(http.StatusBadRequest, "invalid due date")
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
			co, err = c.PutCalendarObject(ctx.Request().Context(), savePath, cal)
			if err != nil {
				return fmt.Errorf("failed to save task: %v", err)
			}

			return ctx.Redirect(http.StatusFound, TaskObject{co}.URL())
		}

		summary, _ := todo.Props.Text("SUMMARY")

		return ctx.Render(http.StatusOK, "update-task.html", &UpdateTaskRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("title.update"), summary)),
			Calendars:      writable,
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

		return ctx.Redirect(http.StatusFound, "/tasks")
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

		co, err := c.GetCalendarObject(ctx.Request().Context(), taskPath)
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

		return ctx.Redirect(http.StatusFound, "/tasks")
	})
}
