package alpscaldav

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"git.sr.ht/~migadu/alps"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type CalendarRenderData struct {
	alps.BaseRenderData
	Time               time.Time
	Now                time.Time
	Dates              [7 * 6]time.Time
	Calendars          []CalendarInfo
	Calendar           *CalendarInfo // first calendar, for the bundled upstream themes
	Events             []CalendarObject
	PrevPage, NextPage string
	PrevTime, NextTime time.Time

	EventsForDate func(time.Time) []CalendarObject
	ColorForPath  func(string) string
	DaySuffix     func(n int) string
	Sub           func(a, b int) int
}

type CalendarDateRenderData struct {
	alps.BaseRenderData
	Time               time.Time
	Events             []CalendarObject
	PrevPage, NextPage string

	ColorForPath func(string) string
}

type EventRenderData struct {
	alps.BaseRenderData
	Calendar *CalendarInfo
	Event    CalendarObject
}

type UpdateEventRenderData struct {
	alps.BaseRenderData
	Calendars      []CalendarInfo
	Calendar       *CalendarInfo
	CalendarObject *caldav.CalendarObject // nil if creating a new event
	Event          *ical.Event
}

type Settings struct {
	CalendarFilter   bool
	VisibleCalendars []string
	ShowCompleted    bool
}

type TaskGroup struct {
	Calendar CalendarInfo
	Tasks    []TaskObject
}

type TasksRenderData struct {
	alps.BaseRenderData
	Calendars     []CalendarInfo
	TaskGroups    []TaskGroup
	ShowCompleted bool
}

type TaskRenderData struct {
	alps.BaseRenderData
	Calendar *CalendarInfo
	Task     TaskObject
}

type UpdateTaskRenderData struct {
	alps.BaseRenderData
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

func parseTime(dateStr, timeStr string) (time.Time, error) {
	layout := inputDateLayout
	s := dateStr
	if timeStr != "" {
		layout = inputDateLayout + "T" + inputTimeLayout
		s = dateStr + "T" + timeStr
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		err = fmt.Errorf("malformed date: %v", err)
		return time.Time{}, echo.NewHTTPError(http.StatusBadRequest, err)
	}
	return t, nil
}

func loadSettings(store alps.Store) (*Settings, error) {
	settings := &Settings{}
	if err := store.Get(settingsKey, settings); err != nil && err != alps.ErrNoStoreEntry {
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
	p.POST("/calendar", func(ctx *alps.Context) error {
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

	p.POST("/tasks", func(ctx *alps.Context) error {
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
		settings.ShowCompleted = params.Get("show-completed") == "1"
		if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
			return fmt.Errorf("failed to save CalDAV settings: %v", err)
		}
		return ctx.Redirect(http.StatusFound, ctx.Request().URL.RequestURI())
	})

	p.GET("/calendar", func(ctx *alps.Context) error {
		var start time.Time
		if s := ctx.QueryParam("month"); s != "" {
			var err error
			start, err = time.Parse(monthPageLayout, s)
			if err != nil {
				return fmt.Errorf("failed to parse month: %v", err)
			}
		} else {
			now := time.Now()
			start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		}
		end := start.AddDate(0, 1, 0)

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
			calInfo := calInfo
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

		// TODO: Time zones are hard
		var dates [7 * 6]time.Time
		initialDate := start.UTC()
		initialDate = initialDate.AddDate(0, 0, -int(initialDate.Weekday()))
		for i := 0; i < len(dates); i++ {
			dates[i] = initialDate
			initialDate = initialDate.AddDate(0, 0, 1)
		}

		eventMap := make(map[time.Time][]CalendarObject)
		for _, ev := range events {
			ev := ev // make a copy
			// TODO: include event on each date for which it is active
			co := ev.Data.Events()[0]
			startTime, _ := co.DateTimeStart(nil)
			startTime = startTime.UTC().Truncate(time.Hour * 24)
			eventMap[startTime] = append(eventMap[startTime], CalendarObject{&ev})
		}

		return ctx.Render(http.StatusOK, "calendar.html", &CalendarRenderData{
			BaseRenderData: *alps.NewBaseRenderData(ctx).
				WithTitle("Calendar: " + start.Format("January 2006")),
			Time:      start,
			Now:       time.Now(), // TODO: Use client time zone
			Calendars: calendarInfos,
			Calendar:  &calendars[0],
			Dates:     dates,
			Events:    newCalendarObjectList(events),
			PrevPage:  start.AddDate(0, -1, 0).Format(monthPageLayout),
			NextPage:  start.AddDate(0, 1, 0).Format(monthPageLayout),
			PrevTime:  start.AddDate(0, -1, 0),
			NextTime:  start.AddDate(0, 1, 0),

			EventsForDate: func(when time.Time) []CalendarObject {
				if events, ok := eventMap[when.Truncate(time.Hour*24)]; ok {
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

	p.GET("/calendar/date", func(ctx *alps.Context) error {
		var start time.Time
		if s := ctx.QueryParam("date"); s != "" {
			var err error
			start, err = time.Parse(datePageLayout, s)
			if err != nil {
				return fmt.Errorf("failed to parse date: %v", err)
			}
		} else {
			now := time.Now()
			start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
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

		var events []caldav.CalendarObject
		for _, cal := range calendars {
			if !cal.SupportsEvent() {
				continue
			}
			if settings.CalendarFilter && !visibleSet[cal.Path] {
				continue
			}
			calEvents, err := c.QueryCalendar(ctx.Request().Context(), cal.Path, &query)
			if err != nil {
				return fmt.Errorf("failed to query calendar %s: %v", cal.Name, err)
			}
			events = append(events, calEvents...)
		}

		return ctx.Render(http.StatusOK, "calendar-date.html", &CalendarDateRenderData{
			BaseRenderData: *alps.NewBaseRenderData(ctx).
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

	p.GET("/calendar/:path", func(ctx *alps.Context) error {
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
			BaseRenderData: *alps.NewBaseRenderData(ctx).WithTitle(summary),
			Calendar:       calendar,
			Event:          CalendarObject{event},
		})
	})

	updateEvent := func(ctx *alps.Context) error {
		calendarObjectPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

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
			start, err := parseTime(ctx.FormValue("start-date"), ctx.FormValue("start-time"))
			if err != nil {
				return err
			}
			end, err := parseTime(ctx.FormValue("end-date"), ctx.FormValue("end-time"))
			if err != nil {
				return err
			}
			if start.After(end) {
				return echo.NewHTTPError(http.StatusBadRequest, "event start is after its end")
			}

			if start == end {
				end = start.Add(24 * time.Hour)
			}

			event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now())
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

			cal := ical.NewCalendar()
			cal.Props.SetText(ical.PropProductID, "-//emersion.fr//alps//EN")
			cal.Props.SetText(ical.PropVersion, "2.0")
			cal.Children = append(cal.Children, event.Component)

			var savePath string
			if co != nil {
				savePath = co.Path
			} else {
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
			BaseRenderData: *alps.NewBaseRenderData(ctx).WithTitle("Update " + summary),
			Calendars:      writable,
			Calendar:       currentCalendar,
			CalendarObject: co,
			Event:          event,
		})
	}

	p.GET("/calendar/create", updateEvent)
	p.POST("/calendar/create", updateEvent)

	p.GET("/calendar/:path/update", updateEvent)
	p.POST("/calendar/:path/update", updateEvent)

	p.POST("/calendar/:path/delete", func(ctx *alps.Context) error {
		path, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		c, _, err := p.clientWithCalendar(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		if err := c.RemoveAll(ctx.Request().Context(), path); err != nil {
			return fmt.Errorf("failed to delete calendar object: %v", err)
		}

		return ctx.Redirect(http.StatusFound, "/calendar")
	})

	// Tasks routes
	p.GET("/tasks", func(ctx *alps.Context) error {
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
			if !cal.SupportsTodo() {
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

		showCompleted := settings.ShowCompleted

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

		var taskGroups []TaskGroup
		for _, cal := range calendarInfos {
			if !cal.Visible {
				continue
			}
			calTasks, err := c.QueryCalendar(ctx.Request().Context(), cal.Path, &query)
			if err != nil {
				return fmt.Errorf("failed to query tasks from %s: %v", cal.Name, err)
			}

			var filtered []caldav.CalendarObject
			for _, task := range calTasks {
				todo := getFirstTodo(task.Data)
				if todo == nil {
					continue
				}
				status, _ := todo.Props.Text("STATUS")
				if status == "COMPLETED" && !showCompleted {
					continue
				}
				filtered = append(filtered, task)
			}

			if len(filtered) > 0 {
				taskGroups = append(taskGroups, TaskGroup{
					Calendar: cal,
					Tasks:    newTaskObjectList(filtered),
				})
			}
		}

		return ctx.Render(http.StatusOK, "tasks.html", &TasksRenderData{
			BaseRenderData: *alps.NewBaseRenderData(ctx).WithTitle("Tasks"),
			Calendars:      calendarInfos,
			TaskGroups:     taskGroups,
			ShowCompleted:  showCompleted,
		})
	})

	p.GET("/tasks/:path", func(ctx *alps.Context) error {
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
			BaseRenderData: *alps.NewBaseRenderData(ctx).WithTitle(summary),
			Calendar:       calendar,
			Task:           TaskObject{task},
		})
	})

	updateTask := func(ctx *alps.Context) error {
		taskPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

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

			todo.Props.SetDateTime(ical.PropDateTimeStamp, time.Now())
			todo.Props.SetText(ical.PropSummary, summary)

			if description != "" {
				description = strings.ReplaceAll(description, "\r", "")
				todo.Props.SetText(ical.PropDescription, description)
			} else {
				todo.Props.Del(ical.PropDescription)
			}

			if dueDate != "" {
				due, err := time.Parse(inputDateLayout, dueDate)
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
				cal.Props.SetText(ical.PropProductID, "-//emersion.fr//alps//EN")
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
			BaseRenderData: *alps.NewBaseRenderData(ctx).WithTitle("Update " + summary),
			Calendars:      writable,
			Calendar:       currentCalendar,
			CalendarObject: co,
			Todo:           todo,
		})
	}

	p.GET("/tasks/create", updateTask)
	p.POST("/tasks/create", updateTask)

	p.GET("/tasks/:path/edit", updateTask)
	p.POST("/tasks/:path/edit", updateTask)

	p.POST("/tasks/:path/delete", func(ctx *alps.Context) error {
		path, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		c, _, err := p.clientWithCalendar(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		if err := c.RemoveAll(ctx.Request().Context(), path); err != nil {
			return fmt.Errorf("failed to delete task: %v", err)
		}

		return ctx.Redirect(http.StatusFound, "/tasks")
	})

	p.POST("/tasks/:path/complete", func(ctx *alps.Context) error {
		taskPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		c, _, err := p.clientWithCalendar(ctx.Request().Context(), ctx.Session)
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
			todo.Props.SetDateTime(ical.PropCompleted, time.Now())
		}
		todo.Props.SetDateTime(ical.PropDateTimeStamp, time.Now())

		_, err = c.PutCalendarObject(ctx.Request().Context(), co.Path, co.Data)
		if err != nil {
			return fmt.Errorf("failed to update task: %v", err)
		}

		return ctx.Redirect(http.StatusFound, "/tasks")
	})
}
