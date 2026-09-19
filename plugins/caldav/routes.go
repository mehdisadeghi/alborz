package alborzcaldav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/labstack/echo/v4"
)

// collectionPage is a calendar's own page: the list it belongs to is
// the tasks rail when it holds no events.
func (p *plugin) collectionPage() dav.Page {
	return dav.Page{
		Base:   "/calendars/",
		List:   "/calendar",
		Color:  calendarColor,
		Ext:    ".ics",
		Forget: p.dav.Forget,
		Show:   show,
		Rail: func(ctx *alborz.Context, list string) (dav.Rail, error) {
			if list == "/tasks" {
				return p.taskRail(ctx)
			}
			return p.eventRail(ctx)
		},
		Import: func(ctx *alborz.Context, path string, raw []byte) (int, error) {
			c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return 0, err
			}
			return importObjects(ctx.Request().Context(), c, path, raw)
		},
		Export: func(ctx *alborz.Context, path string, from, to time.Time) ([]byte, error) {
			c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return nil, err
			}
			return exportCalendar(ctx.Request().Context(), c, path, from, to)
		},
		Lookup: func(ctx *alborz.Context, path string) (dav.Collection, func() int, string, string, error) {
			if isSubscription(path) {
				settings, err := loadSettings(ctx.Session.Store())
				if err != nil {
					return dav.Collection{}, nil, "", "", err
				}
				i := subscriptionAt(settings.Subscriptions, path)
				if i < 0 {
					return dav.Collection{}, nil, "", "", alborz.NotFound("notfound.collection")
				}
				sub := settings.Subscriptions[i]
				return sub.info(ctx.Session.Username()),
					func() int { return subs.count(sub.URL) }, "/calendar", ctx.T("nav.calendar"), nil
			}
			_, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return dav.Collection{}, nil, "", "", err
			}
			info := dav.At(calendars, path)
			if info == nil {
				return dav.Collection{}, nil, "", "", alborz.NotFound("notfound.collection")
			}
			list := collectionList(*info)
			label := ctx.T("nav.calendar")
			if list == "/tasks" {
				label = ctx.T("nav.tasks")
			}
			return *info, p.dav.CountObjects(ctx, info.Path), list, label, nil
		},
	}
}

// forSubscription routes a collection page's POST to the subscription
// handler when the path names one; everything else goes to the server.
func (p *plugin) forSubscription(sub func(*alborz.Context, string) error, next func(*alborz.Context) error) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		path, err := dav.ParseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		path = dav.CanonicalCollectionPath(path)
		if ctx.Request().Method != http.MethodPost || !isSubscription(path) {
			return next(ctx)
		}
		return sub(ctx, path)
	}
}

// updateSubscription saves the name and colour the page was given.
func (p *plugin) updateSubscription(ctx *alborz.Context, path string) error {
	settings, err := loadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	i := subscriptionAt(settings.Subscriptions, path)
	if i < 0 {
		return alborz.NotFound("notfound.collection")
	}
	name := strings.TrimSpace(ctx.FormValue("name"))
	if name == "" {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, ctx.T("form.nameneeded"))
	}
	settings.Subscriptions[i].Name = name
	settings.Subscriptions[i].Color = ctx.FormValue("color")
	if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
		return err
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath("/calendar")))
}

// unsubscribe drops the feed; nothing is deleted anywhere.
func (p *plugin) unsubscribe(ctx *alborz.Context, path string) error {
	settings, err := loadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	i := subscriptionAt(settings.Subscriptions, path)
	if i < 0 {
		return alborz.NotFound("notfound.collection")
	}
	name := settings.Subscriptions[i].Name
	settings.Subscriptions = append(settings.Subscriptions[:i], settings.Subscriptions[i+1:]...)
	if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
		return err
	}
	ctx.PutNotice(fmt.Sprintf(ctx.T("notice.unsubscribedcalendar"), name))
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/calendar"))
}

// collectionList is the page a collection belongs to.
func collectionList(info dav.Collection) string {
	if !supportsEvent(info.Components) {
		return "/tasks"
	}
	return "/calendar"
}

// createForm is the form for a new calendar. A task list is a calendar
// whose component set holds VTODO; RFC 4791 knows no other kind of
// collection. One form makes both, entered from whichever rail wants
// one, and the tasks entrance offers no choice of what it holds.
func (p *plugin) createForm(ctx *alborz.Context) (dav.CreateForm, error) {
	forTasks := ctx.QueryParam("for") == "tasks"
	// Back to the rail it was asked for, when the new collection shows
	// there; a task list never appears under calendars.
	made := func(holds string) (string, string) {
		if holds == "tasks" {
			return ctx.T("notice.tasklistcreated"), "/tasks"
		}
		return ctx.T("notice.calendarcreated"), "/calendar"
	}
	if forTasks {
		rail, err := p.taskRail(ctx)
		return dav.CreateForm{Rail: rail, Title: ctx.T("tasks.newlist"), Section: ctx.T("nav.tasks"), List: "/tasks",
			Holds: "tasks", Made: made}, err
	}
	rail, err := p.eventRail(ctx)
	return dav.CreateForm{Rail: rail, Title: ctx.T("calendar.newcalendar"), Section: ctx.T("nav.calendar"), List: "/calendar",
		Holds: "both", OffersHolds: true, Made: made}, err
}

// SubscribeRenderData renders subscribe-calendar.html.
type SubscribeRenderData struct {
	alborz.BaseRenderData
	Rail     dav.Rail
	Accounts []alborz.Account
	Account  string
	Address  string
	Next     string
	Error    string
}

// handleSubscribe follows a calendar by its address. The feed is fetched
// once now, so a bad address is refused here and a good one shows on
// the next view without waiting for the poll; the feed names itself,
// and the colour is changed on the calendar's own page.
func handleSubscribe(p *plugin) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		rail, err := p.eventRail(ctx)
		if err != nil {
			return err
		}
		data := &SubscribeRenderData{
			Rail:           rail,
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("calendar.subscribe")),
			Accounts:       ctx.Accounts(),
			Account:        ctx.Session.Username(),
			Next:           ctx.FormValue("next"),
		}
		if ctx.Request().Method != http.MethodPost {
			return ctx.Render(http.StatusOK, "subscribe-calendar.html", data)
		}
		data.Address = strings.TrimSpace(ctx.FormValue("address"))
		if account := ctx.FormValue("account"); account != "" {
			data.Account = account
		}
		if data.Address == "" {
			data.Error = ctx.T("calendar.subscribeurlneeded")
			return ctx.Render(http.StatusUnprocessableEntity, "subscribe-calendar.html", data)
		}
		session := ctx.SessionFor(data.Account)
		if session == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
		}
		sub := Subscription{URL: normalizeFeedURL(data.Address), Color: dav.DefaultColor}
		settings, err := loadSettings(session.Store())
		if err != nil {
			return err
		}
		if subscriptionAt(settings.Subscriptions, sub.path()) >= 0 {
			data.Error = fmt.Sprintf(ctx.T("calendar.alreadysubscribed"), sub.URL)
			return ctx.Render(http.StatusUnprocessableEntity, "subscribe-calendar.html", data)
		}
		if err := subs.refresh(sub.URL); err != nil {
			data.Error = fmt.Sprintf(ctx.T("calendar.feedfailed"), sub.URL, err)
			if errors.Is(err, errNotFeed) {
				data.Error = fmt.Sprintf(ctx.T("calendar.notfeed"), sub.URL)
			}
			return ctx.Render(http.StatusUnprocessableEntity, "subscribe-calendar.html", data)
		}
		sub.Name = subs.name(sub.URL)
		if sub.Name == "" {
			sub.Name = feedHost(sub.URL)
		}
		settings.Subscriptions = append(settings.Subscriptions, sub)
		if err := session.Store().Put(settingsKey, settings); err != nil {
			return err
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
}

// eventListParams are what decide which events a view holds: an event's
// page is opened with them and returns to them.
var eventListParams = []string{"account", "cal", "month", "date", "view", "span"}

type EventRenderData struct {
	alborz.BaseRenderData
	Rail     dav.Rail
	Calendar *dav.Collection
	Event    CalendarObject
	// List is the view the page was opened from; see TaskRenderData.
	List       string
	Star       string
	Neighbours dav.Neighbours
	// Repeats says the event's rule in words, or nothing, and
	// RepeatsUntil its last day, kept apart to be read left to right.
	Repeats, RepeatsUntil string
}

type UpdateEventRenderData struct {
	alborz.BaseRenderData
	Rail           dav.Rail
	Groups         []dav.Group
	Calendar       *dav.Collection
	CalendarObject *caldav.CalendarObject // nil if creating a new event
	Event          *ical.Event

	// The two shapes the form offers, filled whichever one applies so
	// that ticking the box does not lose what was already typed.
	AllDay    bool
	StartTime string // datetime-local
	EndTime   string
	StartDate string // date, and the last day rather than the day after
	EndDate   string
	// Attendees are the addresses invited, one per line. Naming anybody
	// makes this account the organizer and sends an invitation on save.
	Attendees string
	Repeat    Repeat
	// RepeatFreqs are the rules the form offers, in its order.
	RepeatFreqs []string

	// Error is shown as an alert on the form just submitted: invalid
	// input is answered by the page itself, never by a status page.
	Error string
}

type Settings struct {
	CalendarFilter   bool
	VisibleCalendars []string
	TaskFilter       bool
	VisibleTasks     []string
	Subscriptions    []Subscription
}

// MoveTasksRenderData is the chooser a move without a destination
// answers with: the selection it will act on, and the lists it may go to.
type MoveTasksRenderData struct {
	alborz.BaseRenderData
	Rail      dav.Rail
	Calendars []dav.Collection
	Paths     []string
	Next      string
}

type TasksRenderData struct {
	alborz.BaseRenderData
	Calendars []dav.Collection
	Tasks     []TaskRow
	// Quick is the list a task typed above the rows goes into; nil
	// when no list takes tasks.
	Quick      *quickList
	View       string
	Filters    []alborz.Filter
	FilterRows []alborz.FilterRow
	Query      string
	Sorting    dav.Sorting
}

// TaskRow is the flat, table-shaped representation shared by the task
// list and its sort controls. Calendar ownership stays explicit instead
// of being encoded as nested visual groups.
type TaskRow struct {
	Task     TaskObject
	Calendar dav.Collection
	Summary  string
	Status   string
	Due      time.Time
	// Added is CREATED (RFC 5545 3.8.7.1), which every task alborz has
	// seen carries and which costs nothing to read: it is in the data
	// the list already fetched. Zero where the writer left it out.
	Added     time.Time
	Completed bool
	// Priority is the band the task's PRIORITY falls in, or empty.
	Priority string
	// Star is the colour the task is marked in, or empty.
	Star string
	// Href is the task's own page with the list carried along, so that
	// page can name the tasks either side of it.
	Href string
}

// taskListParams are what decide which tasks a list holds and in what
// order: a task's page is opened with them and returns to them.
var taskListParams = []string{"account", "cal", "query", "view", "sort", "dir", "page", "ipp"}

type TaskRenderData struct {
	alborz.BaseRenderData
	Rail     dav.Rail
	Calendar *dav.Collection
	Task     TaskObject
	// List is the list the page was opened from, filter and order kept,
	// for what leaves the page with nothing to come back to.
	List       string
	Star       string
	Priority   string
	Neighbours dav.Neighbours
}

type UpdateTaskRenderData struct {
	alborz.BaseRenderData
	Rail           dav.Rail
	Groups         []dav.Group
	Calendar       *dav.Collection
	CalendarObject *caldav.CalendarObject
	Todo           *ical.Component
	// Due is the due date as the field holds it: what was typed, or
	// what the task has.
	Due string
	// Priority is the band the select holds, and PriorityBands the
	// bands it offers, in order.
	Priority      string
	PriorityBands []string
	Error         string
}

const (
	datePageLayout = "2006-01-02"
	settingsKey    = "caldav.settings"
)

func init() {
	alborz.KeepKey(settingsKey)
}

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

// newEventStart reads the ?date= a create link carries and keeps the
// current time of day on it, so a form opened from a day lands on that
// day rather than on today.
func newEventStart(ctx *alborz.Context, loc *time.Location) time.Time {
	now := time.Now().In(loc)
	d, err := time.ParseInLocation(datePageLayout, ctx.QueryParam("date"), loc)
	if err != nil {
		return now
	}
	return time.Date(d.Year(), d.Month(), d.Day(), now.Hour(), now.Minute(), 0, 0, loc)
}

// fillEventForm gives the form both shapes of the same event: the times
// it has, and the days it covers. Ticking the box then costs nothing
// that was already entered. The last day is shown, not the day after -
// DTEND is exclusive in iCalendar and inclusive in every head.
//
// when is the day a new event starts on: the page that opened the form
// knows which day the reader is looking at, and typing it again is the
// only alternative. It is ignored once the event has a start of its own.
func fillEventForm(ctx *alborz.Context, d *UpdateEventRenderData, loc *time.Location, when time.Time) {
	d.RepeatFreqs = repeatFreqs
	d.Repeat = formRepeat(ctx, d.Event, loc)
	start, _ := d.Event.DateTimeStart(loc)
	end, _ := d.Event.DateTimeEnd(loc)
	if prop := d.Event.Props.Get(ical.PropDateTimeStart); prop != nil {
		d.AllDay = prop.ValueType() == ical.ValueDate
	}
	if start.IsZero() {
		start = when
	}
	if !end.After(start) {
		end = start.Add(time.Hour)
	}
	d.StartTime = d.GlobalData.InputDateTime(start)
	d.EndTime = d.GlobalData.InputDateTime(end)
	d.StartDate = d.GlobalData.InputDate(start)
	last := end
	if d.AllDay {
		last = end.AddDate(0, 0, -1)
		if last.Before(start) {
			last = start
		}
	}
	d.EndDate = d.GlobalData.InputDate(last)
	d.Attendees = attendeeLines(d.Event)
}

func loadSettings(store alborz.Store) (*Settings, error) {
	settings := &Settings{}
	if err := store.Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	return settings, nil
}

// choose writes the collections the reader ticked into the settings.
func choose(set func(*Settings, []string)) func(alborz.Store, []string) error {
	return func(store alborz.Store, paths []string) error {
		settings, err := loadSettings(store)
		if err != nil {
			return err
		}
		set(settings, paths)
		return store.Put(settingsKey, settings)
	}
}

// show ticks a collection the account just made or accepted in the
// chosen sets in force: what to see was chosen before it existed, which
// was not a choice to hide it.
func show(store alborz.Store, path string) error {
	settings, err := loadSettings(store)
	if err != nil {
		return err
	}
	if !settings.CalendarFilter && !settings.TaskFilter {
		return nil
	}
	if settings.CalendarFilter {
		settings.VisibleCalendars = append(settings.VisibleCalendars, path)
	}
	if settings.TaskFilter {
		settings.VisibleTasks = append(settings.VisibleTasks, path)
	}
	return store.Put(settingsKey, settings)
}

func firstEvent(cal *ical.Calendar) *ical.Component {
	if evs := cal.Events(); len(evs) > 0 {
		return evs[0].Component
	}
	return nil
}

func getFirstTodo(cal *ical.Calendar) *ical.Component {
	for _, child := range cal.Children {
		if child.Name == ical.CompToDo {
			return child
		}
	}
	return nil
}

// eventVisibility is the calendar pages' answer to visibleCalendars:
// which calendars the account chose to see.
func eventVisibility(s *Settings) (bool, []string) { return s.CalendarFilter, s.VisibleCalendars }
func taskVisibility(s *Settings) (bool, []string)  { return s.TaskFilter, s.VisibleTasks }

// eventRail and taskRail are the section's rail for a page that is not
// its list, listing every account's calendars of the kind.
func (p *plugin) eventRail(ctx *alborz.Context) (dav.Rail, error) {
	rail, err := p.calendarRail(ctx, supportsEvent, eventVisibility)
	next := url.QueryEscape(ctx.Request().URL.RequestURI())
	rail.Path, rail.Action = "/calendar", "/calendar"
	rail.NewHref, rail.NewLabel = "/calendars/create?next="+next, ctx.T("calendar.newcalendar")
	rail.FollowHref, rail.FollowLabel = "/calendars/subscribe?next="+next, ctx.T("calendar.subscribe")
	rail.ImportHref, rail.ImportLabel = "/calendar/import", ctx.T("calendar.import")
	return rail, err
}

func (p *plugin) taskRail(ctx *alborz.Context) (dav.Rail, error) {
	rail, err := p.calendarRail(ctx, supportsTodo, taskVisibility)
	rail.Path, rail.Action = "/tasks", "/tasks"
	rail.NewHref, rail.NewLabel = "/calendars/create?for=tasks&next="+url.QueryEscape(ctx.Request().URL.RequestURI()), ctx.T("tasks.newlist")
	rail.ImportHref, rail.ImportLabel = "/tasks/import", ctx.T("tasks.import")
	return rail, err
}

func (p *plugin) calendarRail(ctx *alborz.Context, kind func([]string) bool, chosen func(*Settings) (bool, []string)) (dav.Rail, error) {
	rail := dav.Rail{Field: "cal", ItemClass: "calendar-item", EditHref: "/calendars/"}
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return rail, err
	}
	infos, _, err := visibleCalendars(accounts, ctx.URLAccount(), nil, kind, chosen)
	rail.Items = infos
	return rail, err
}

// visibleCalendars are dav.Visible's calendars of one kind, with the
// feeds the account follows beside the server's.
func visibleCalendars(accounts []dav.Account[*caldav.Client], scope string, only map[string]bool, kind func([]string) bool, chosen func(*Settings) (filter bool, paths []string)) ([]dav.Collection, []dav.Site[*caldav.Client], error) {
	return dav.Visible(accounts, scope, only, func(acc dav.Account[*caldav.Client]) ([]dav.Collection, bool, []string, error) {
		settings, err := loadSettings(acc.Session.Store())
		if err != nil {
			return nil, false, nil, fmt.Errorf("failed to load CalDAV settings: %w", err)
		}
		var cals []dav.Collection
		for _, cal := range acc.Collections {
			if kind(cal.Components) {
				cals = append(cals, cal)
			}
		}
		for _, sub := range settings.Subscriptions {
			if cal := sub.info(acc.Name); kind(cal.Components) {
				cals = append(cals, cal)
			}
		}
		filter, paths := chosen(settings)
		return cals, filter, paths, nil
	})
}

// eventQuery asks for the events between two instants, expanded by the
// server where it can.
func eventQuery(start, end time.Time) caldav.CalendarQuery {
	return caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:  "VCALENDAR",
			Props: []string{"VERSION"},
			Comps: []caldav.CalendarCompRequest{{
				Name:  "VEVENT",
				Props: []string{"SUMMARY", "UID", "DTSTART", "DTEND", "DURATION", "COLOR"},
			}},
			Expand: &caldav.CalendarExpandRequest{Start: start, End: end},
		},
		CompFilter: caldav.CompFilter{
			Name:  "VCALENDAR",
			Comps: []caldav.CompFilter{{Name: "VEVENT", Start: start, End: end}},
		},
	}
}

func registerRoutes(p *plugin) {
	guard := func(h func(*alborz.Context) error) func(*alborz.Context) error {
		return p.dav.Guarded(errNoCalendar, "calendar.unconfigured", "/calendars/create", h)
	}
	GET := func(path string, h func(*alborz.Context) error) { p.GoPlugin.GET(path, guard(h)) }
	POST := func(path string, h func(*alborz.Context) error) { p.GoPlugin.POST(path, guard(h)) }
	POST("/calendar", dav.HandleChoose("/calendar", "cal", choose(func(s *Settings, paths []string) {
		s.CalendarFilter, s.VisibleCalendars = true, paths
	})))
	POST("/tasks", dav.HandleChoose("/tasks", "cal", choose(func(s *Settings, paths []string) {
		s.TaskFilter, s.VisibleTasks = true, paths
	})))
	POST("/calendar/refresh", p.dav.HandleRefresh("/calendar"))
	POST("/tasks/refresh", p.dav.HandleRefresh("/tasks"))
	GET("/calendar", p.month)
	GET("/calendar/export", p.exportMonth)
	GET("/calendar/date", p.day)
	GET("/calendar/:path", p.event)

	GET("/calendar/:path/raw", p.rawObject)
	GET("/tasks/:path/raw", p.rawObject)
	GET("/calendars/subscribe", handleSubscribe(p))
	POST("/calendars/subscribe", handleSubscribe(p))
	page := p.collectionPage()
	GET("/calendars/create", page.HandleCreate(p.dav, p.createForm))
	POST("/calendars/create", page.HandleCreate(p.dav, p.createForm))
	for _, method := range []func(string, func(*alborz.Context) error){GET, POST} {
		method("/calendar/import", page.HandleImportPage(p.dav, "/calendar", "nav.calendar", "calendar.import", "calendar.importhint", "event", true))
		method("/tasks/import", page.HandleImportPage(p.dav, "/tasks", "nav.tasks", "tasks.import", "tasks.importhint", "task", false))
	}
	GET("/calendars/:path", page.Handle(p.dav))
	POST("/calendars/:path", p.forSubscription(p.updateSubscription, page.Handle(p.dav)))
	POST("/calendars/:path/delete", p.forSubscription(p.unsubscribe, page.HandleDelete(p.dav)))
	POST("/calendars/:path/import", page.HandleImport(p.dav))
	GET("/calendars/:path/export", page.HandleExport(p.dav))
	POST("/calendars/:path/share", page.HandleShare(p.dav))
	POST("/calendars/:path/unshare", page.HandleUnshare(p.dav))
	POST("/calendars/:path/accept", page.HandleAnswer(p.dav, true))
	POST("/calendars/:path/decline", page.HandleAnswer(p.dav, false))
	POST("/calendars/:path/publish", page.HandlePublish(p.dav, true))
	POST("/calendars/:path/unpublish", page.HandlePublish(p.dav, false))
	p.Inject("*", p.dav.InjectInvitations("cal", page.Base, "/calendar", "/calendars", "/tasks"))
	GET("/calendar/create", p.updateEvent)
	POST("/calendar/create", p.updateEvent)
	GET("/calendar/:path/update", p.updateEvent)
	POST("/calendar/:path/update", p.updateEvent)
	remove := func(list string) func(*alborz.Context) error {
		return dav.Handler(dav.Action[*caldav.Client]{Client: p.client, Do: dav.Delete[*caldav.Client], List: list})
	}
	POST("/calendar/delete", remove("/calendar"))
	POST("/calendar/:path/delete", remove("/calendar"))
	GET("/tasks", p.tasks)
	GET("/tasks/:path", p.task)

	GET("/tasks/create", p.updateTask)
	POST("/tasks/create", p.updateTask)
	GET("/tasks/:path/edit", p.updateTask)
	POST("/tasks/:path/edit", p.updateTask)
	POST("/tasks/:path/delete", remove("/tasks"))
	POST("/tasks/delete", remove("/tasks"))
	POST("/tasks/complete", p.complete)
	POST("/tasks/move", p.move)
	POST("/tasks/export", dav.HandleExport(p.client, "/tasks",
		func(ctx *alborz.Context) string { return ctx.T("nav.tasks") + ".ics" }, joinCalendars))

	POST("/tasks/:path/note", p.note(getFirstTodo, "/tasks"))
	POST("/calendar/:path/note", p.note(firstEvent, "/calendar"))
	POST("/tasks/:path/complete", p.complete)
	POST("/tasks/:path/color", p.color(getFirstTodo, "/tasks"))
	POST("/calendar/:path/color", p.color(firstEvent, "/calendar"))
}

// A line added to what the object already says, from its own page.
// The addition is appended and dated rather than replacing what is
// there: a note field people actually use is a record, and the edit
// form remains the way to rewrite one.
func (p *plugin) note(comp func(*ical.Calendar) *ical.Component, list string) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		note := strings.TrimSpace(ctx.FormValue("note"))
		return dav.Run(ctx, dav.Action[*caldav.Client]{Client: p.client, List: list,
			Do: func(ctx *alborz.Context, ref dav.Ref[*caldav.Client]) error {
				if note == "" {
					return nil
				}
				_, err := changeComponent(ctx, ref, comp, func(target *ical.Component) {
					existing, _ := target.Props.Text(ical.PropDescription)
					target.Props.SetText(ical.PropDescription, alborz.AppendNote(existing, note, time.Now()))
				})
				return err
			}})
	}
}

// changeComponent reads one object, lets change rewrite the component
// comp finds in it, stamps that and writes the object back.
func changeComponent(ctx *alborz.Context, ref dav.Ref[*caldav.Client], comp func(*ical.Calendar) *ical.Component, change func(*ical.Component)) (*caldav.CalendarObject, error) {
	co, err := getCalendarObject(ctx, ref.Client, ref.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to get object: %v", err)
	}
	target := comp(co.Data)
	if target == nil {
		return nil, errors.New("the object holds no such component")
	}
	change(target)
	target.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	target.Props.SetDateTime(ical.PropLastModified, time.Now().UTC())
	_, err = ref.Client.PutCalendarObject(ctx.Request().Context(), co.Path, co.Data, nil)
	return co, err
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
			if view != "list" && !starMatches(oc.Star(), view) {
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
		CollectionFor:  collection,
		CollectionHref: href,
	})
}

// calendarFilters is what an agenda or a task list is narrowed by, as
// chips above the rows: the star and the calendar the URL names.
func calendarFilters(ctx *alborz.Context, calendars []dav.Collection) []alborz.Filter {
	var out []alborz.Filter
	if f, ok := ctx.FilterOn("view", ctx.T("filter.color")); ok && f.Value != "list" {
		switch f.Value {
		case viewCompleted:
			f.Label, f.Value = ctx.T("tasks.status"), ctx.T("tasks.completed")
		case viewAll:
			f.Label, f.Value = ctx.T("tasks.status"), ctx.T("tasks.all")
		case viewHigh:
			f.Label, f.Value = ctx.T("tasks.priority"), ctx.T("tasks.priority.high")
		case alborzbase.ViewStarred:
			f.Value = ctx.T("mailbox.starred")
		default:
			f.Value = ctx.T("color." + f.Value)
		}
		// Cleared of its star the agenda is still the agenda, not the
		// grid; a task list has no such second self.
		if ctx.Request().URL.Path == "/calendar" {
			f.Href = ctx.WithParam("view", "list")
		}
		out = append(out, f)
	}
	if f, ok := ctx.FilterOn("cal", ctx.T("common.calendar")); ok {
		// The URL names a calendar by path; the chip names it the way
		// the reader does.
		if cal := dav.At(calendars, dav.CanonicalCollectionPath(f.Value)); cal != nil {
			f.Value = cal.Name
		}
		out = append(out, f)
	}
	return out
}

// dayQuery and monthQuery are what an event's page is opened with: the
// list it came from, named in full. The month and the day both have a
// default the URL leaves out, and an event page cannot rebuild a list
// it was not told about.
func dayQuery(ctx *alborz.Context, start time.Time) string {
	q := dav.ListParams(ctx, "account", "cal")
	q.Set("date", start.Format(datePageLayout))
	return q.Encode()
}

func monthQuery(ctx *alborz.Context, mv monthView) string {
	q := dav.ListParams(ctx, "account", "cal", "span", "group")
	q.Set("month", mv.page)
	if mv.view != "" {
		q.Set("view", mv.view)
	}
	return q.Encode()
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

// The task list's own views, beside the stars: open tasks are the
// list itself and no view.
const (
	viewCompleted = "completed"
	viewAll       = "all"
	viewHigh      = "high"
)

// The bands RFC 5545 3.8.1.9 draws over PRIORITY: 1 to 4 high, 5
// medium, 6 to 9 low, 0 or absent none. Apple writes 1, 5 and 9.
const (
	priorityHigh   = "high"
	priorityMedium = "medium"
	priorityLow    = "low"
)

var priorityBands = []string{priorityHigh, priorityMedium, priorityLow}

func priorityBand(todo *ical.Component) string {
	prop := todo.Props.Get(ical.PropPriority)
	if prop == nil {
		return ""
	}
	n, err := strconv.Atoi(prop.Value)
	switch {
	case err != nil || n <= 0:
		return ""
	case n <= 4:
		return priorityHigh
	case n == 5:
		return priorityMedium
	default:
		return priorityLow
	}
}

// setPriorityBand writes a band as Apple does, and leaves a number
// another client wrote alone while it still falls in the band chosen.
func setPriorityBand(todo *ical.Component, band string) {
	if band == priorityBand(todo) {
		return
	}
	value := map[string]string{priorityHigh: "1", priorityMedium: "5", priorityLow: "9"}[band]
	if value == "" {
		todo.Props.Del(ical.PropPriority)
		return
	}
	prop := ical.NewProp(ical.PropPriority)
	prop.Value = value
	todo.Props.Set(prop)
}

// quickList is where a task typed above the list goes: the first list
// the page shows that takes tasks, in the account the page is scoped
// to. A page showing no such list has no line.
type quickList struct {
	Account string
	Path    string
	Name    string
	// Lists are the task lists the line can write into, grouped by
	// account: where a task goes is the reader's to say, on the line
	// they are typing it on.
	Lists []dav.Group
}

func quickListOf(ctx *alborz.Context, calendars []dav.Collection) *quickList {
	scope := ctx.URLAccount()
	var quick *quickList
	var lists []dav.Group
	for _, cal := range calendars {
		if !cal.Writable || !supportsTodo(cal.Components) || (scope != "" && cal.Account != scope) {
			continue
		}
		// The list in force is the one a task lands in; failing that,
		// the first the page shows.
		if quick == nil && cal.Shown {
			quick = &quickList{Account: cal.Account, Path: cal.Path, Name: cal.Name}
		}
		at := -1
		for i := range lists {
			if lists[i].Account == cal.Account {
				at = i
			}
		}
		if at < 0 {
			lists = append(lists, dav.Group{Account: cal.Account})
			at = len(lists) - 1
		}
		lists[at].Collections = append(lists[at].Collections, cal)
	}
	if quick == nil {
		return nil
	}
	quick.Lists = lists
	return quick
}

// taskRows is the task list's filter menu: the completed tasks, every
// task, then the star views.
func taskRows(ctx *alborz.Context, view string) []alborz.FilterRow {
	clear := ctx.WithoutParam("view")
	row := func(label, name string) alborz.FilterRow {
		href := ctx.WithParam("view", name)
		if name == view {
			href = clear
		}
		return alborz.FilterRow{Label: label, Href: href, Current: name == view}
	}
	rows := []alborz.FilterRow{
		row(ctx.T("tasks.completed"), viewCompleted),
		row(ctx.T("tasks.all"), viewAll),
		row(ctx.T("tasks.priorityhigh"), viewHigh),
	}
	return append(rows, alborzbase.ViewRows(ctx, view, false, clear)...)
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

// eventKey names one row of an event list. An object's path is enough
// for a calendar, where a recurring event is one object however many
// times it is drawn; a subscribed feed is a single object holding every
// event, so there the UID within it is what tells them apart.
func eventKey(path, uid string, feed bool) string {
	if feed {
		return path + "#" + uid
	}
	return path
}

// eventItems are the events of the list the reader came from - the day
// where the URL names one, the agenda where it names that - in the
// order that list showed them. An event reached without a list has no
// neighbours, and says so by having none.
func (p *plugin) eventItems(ctx *alborz.Context) ([]dav.Item, error) {
	var shown []Occurrence
	switch {
	case ctx.QueryParam("date") != "":
		dv, err := p.dayView(ctx)
		if err != nil {
			return nil, err
		}
		shown = dv.events
	case ctx.QueryParam("view") != "":
		mv, err := p.monthView(ctx)
		if err != nil {
			return nil, err
		}
		shown = mv.shown()
	default:
		return nil, nil
	}

	params := dav.ListParams(ctx, eventListParams...)
	var items []dav.Item
	seen := map[string]bool{}
	for _, oc := range shown {
		uid, _ := oc.Event.Props.Text("UID")
		key := eventKey(oc.Path, uid, oc.ReadOnly)
		if seen[key] {
			continue
		}
		seen[key] = true
		href := dav.ObjectURL("/calendar/", oc.Path, oc.Account, params)
		if oc.ReadOnly {
			href = dav.ObjectURL("/calendar/", oc.Path, oc.Account, withUID(params, uid))
		}
		items = append(items, dav.Item{Path: key, URL: href})
	}
	return items, nil
}

// withUID names the event within a subscribed feed, which is one object
// holding all of them.
func withUID(params url.Values, uid string) url.Values {
	q := url.Values{"uid": {uid}}
	for key, values := range params {
		q[key] = values
	}
	return q
}

func (p *plugin) event(ctx *alborz.Context) error {
	path, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	c, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
	if err != nil {
		return err
	}
	if strings.Contains(path, "://") {
		return p.feedEvent(ctx, path)
	}

	calendar := dav.Holding(calendars, "", path)
	if calendar == nil {
		if len(calendars) == 0 {
			return errNoCalendar
		}
		calendar = &calendars[0]
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
					"COLOR",
				},
			}},
		},
	}

	events, err := c.MultiGetCalendar(ctx.Request().Context(), path, &multiGet)
	if err != nil {
		return fmt.Errorf("failed to multi-get calendar: %v", err)
	}
	if len(events) == 0 {
		return alborz.NotFound("notfound.event")
	}
	if len(events) != 1 {
		return fmt.Errorf("expected exactly one calendar object with path %q, got %v", path, len(events))
	}
	event := &events[0]
	vevents := event.Data.Events()
	if len(vevents) == 0 {
		return alborz.NotFound("notfound.event")
	}
	summary, _ := vevents[0].Props.Text("SUMMARY")

	rail, err := p.eventRail(ctx)
	if err != nil {
		return err
	}
	items, err := p.eventItems(ctx)
	if err != nil {
		return err
	}
	data := &EventRenderData{
		Rail:           rail,
		List:           eventList(ctx),
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(summary),
		Calendar:       calendar,
		Event:          CalendarObject{CalendarObject: event},
		Star:           componentColor(vevents[0].Component),
		Neighbours:     dav.Around(items, eventKey(path, "", false)),
	}
	data.Repeats, data.RepeatsUntil = repeatWords(ctx, &vevents[0], alborzbase.UserLocation(ctx))
	return ctx.Render(http.StatusOK, "event.html", data)
}

// feedEvent shows one event of a subscribed feed. The feed is one
// object holding every event, so the URL names the event by UID and
// the page is given a calendar holding that event alone, with the
// zones it may refer to. Nothing on it can be edited.
func (p *plugin) feedEvent(ctx *alborz.Context, address string) error {
	info, cal, err := feedObject(ctx, address, ctx.QueryParam("uid"))
	if err != nil {
		return err
	}
	event := cal.Events()[0]
	summary, _ := event.Props.Text(ical.PropSummary)
	rail, err := p.eventRail(ctx)
	if err != nil {
		return err
	}
	items, err := p.eventItems(ctx)
	if err != nil {
		return err
	}
	data := &EventRenderData{
		Rail:           rail,
		List:           eventList(ctx),
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(summary),
		Calendar:       info,
		Event:          CalendarObject{CalendarObject: &caldav.CalendarObject{Path: address, Data: cal}, Color: info.Color, ReadOnly: true},
		Neighbours:     dav.Around(items, eventKey(address, ctx.QueryParam("uid"), true)),
	}
	data.Repeats, data.RepeatsUntil = repeatWords(ctx, &event, alborzbase.UserLocation(ctx))
	return ctx.Render(http.StatusOK, "event.html", data)
}

// feedObject is one event of a subscribed feed as a calendar of its
// own, with the zones it may refer to. A subscription is the account's
// own, kept in its settings beside the server's calendars rather than
// among them.
func feedObject(ctx *alborz.Context, address, uid string) (*dav.Collection, *ical.Calendar, error) {
	settings, err := loadSettings(ctx.Session.Store())
	if err != nil {
		return nil, nil, err
	}
	var info *dav.Collection
	for _, s := range settings.Subscriptions {
		if s.URL == address {
			i := s.info(ctx.Session.Username())
			info = &i
		}
	}
	if info == nil {
		return nil, nil, alborz.NotFound("notfound.subscription", address)
	}
	feed, ok := subs.events(address, info.Color)
	if !ok {
		return nil, nil, alborz.NotFound("notfound.feed", address)
	}
	cal := ical.NewCalendar()
	cal.Props = feed.Data.Props
	for _, ch := range feed.Data.Children {
		id, _ := ch.Props.Text(ical.PropUID)
		if ch.Name == ical.CompTimezone || (ch.Name == ical.CompEvent && id == uid) {
			cal.Children = append(cal.Children, ch)
		}
	}
	if len(cal.Events()) == 0 {
		return nil, nil, alborz.NotFound("notfound.event")
	}
	return info, cal, nil
}

func (p *plugin) updateEvent(ctx *alborz.Context) error {
	calendarObjectPath, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	loc := alborzbase.UserLocation(ctx)

	var c *caldav.Client
	var calendars []dav.Collection
	var groups []dav.Group
	var co *caldav.CalendarObject
	var event *ical.Event
	var currentCalendar *dav.Collection
	if calendarObjectPath != "" {
		c, calendars, err = p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}
		co, err = getCalendarObject(ctx, c, calendarObjectPath)
		if err != nil {
			return fmt.Errorf("failed to get CalDAV event: %v", err)
		}
		// A recurring event with rewritten instances holds several
		// VEVENTs; the one without RECURRENCE-ID is the series, and
		// the form edits that.
		events := co.Data.Events()
		for i := range events {
			if events[i].Props.Get(ical.PropRecurrenceID) == nil {
				event = &events[i]
			}
		}
		if event == nil {
			return fmt.Errorf("calendar object %q holds no event to edit", calendarObjectPath)
		}
		currentCalendar = dav.Holding(calendars, "", co.Path)
	} else {
		// Creating is pooled: it must not fail merely because the active
		// account has no CalDAV collection of this kind.
		groups, err = p.writableGroups(ctx, supportsEvent)
		if err != nil {
			return err
		}
		if len(groups) == 0 || len(groups[0].Collections) == 0 {
			return alborz.RenderInfo(ctx, http.StatusOK, ctx.T("calendar.nowritable"))
		}
		event = ical.NewEvent()
		event.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
		currentCalendar = &groups[0].Collections[0]
	}

	if ctx.Request().Method == "POST" {
		summary := ctx.FormValue("summary")
		description := ctx.FormValue("description")

		// The form answers its own invalid input: the same page with
		// an alert, never a status page the browser writes.
		reject := func(message string) error {
			rail, err := p.eventRail(ctx)
			if err != nil {
				return err
			}
			return ctx.Render(http.StatusUnprocessableEntity, "update-event.html", &UpdateEventRenderData{
				Rail:           rail,
				BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("calendar.createtitle")),
				Groups:         groups,
				Calendar:       currentCalendar,
				CalendarObject: co,
				Event:          event,
				AllDay:         ctx.FormValue("allday") != "",
				StartTime:      ctx.FormValue("start"),
				EndTime:        ctx.FormValue("end"),
				StartDate:      ctx.FormValue("start_date"),
				EndDate:        ctx.FormValue("end_date"),
				Repeat:         Repeat{Freq: ctx.FormValue("repeat"), Until: ctx.FormValue("until")},
				RepeatFreqs:    repeatFreqs,
				Error:          message,
			})
		}
		if summary == "" {
			return reject(ctx.T("form.summaryneeded"))
		}

		to := dav.Ref[*caldav.Client]{Client: c}
		creating := co == nil
		if creating {
			to, err = p.destination(ctx, ctx.FormValue("calendar"), supportsEvent)
			if errors.Is(err, dav.ErrNoDestination) {
				return reject(ctx.T("form.destinationneeded"))
			} else if err != nil {
				return err
			}
		}

		// An event that occupies whole days has no time of day to
		// ask for, and asking anyway is what made a two-day
		// conference impossible to enter.
		allDay := ctx.FormValue("allday") != ""
		var start, end time.Time
		if allDay {
			start, err = ctx.ReadDate(ctx.FormValue("start_date"), loc)
			if err != nil {
				return reject(ctx.T("form.datesneeded"))
			}
			end, err = ctx.ReadDate(ctx.FormValue("end_date"), loc)
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
			start, err = ctx.ReadDateTime(ctx.FormValue("start"), loc)
			if err != nil {
				return reject(ctx.T("form.datesneeded"))
			}
			end, err = ctx.ReadDateTime(ctx.FormValue("end"), loc)
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
		if err := setRepeat(ctx, event, Repeat{Freq: ctx.FormValue("repeat"), Until: ctx.FormValue("until")}, start, allDay, loc); err != nil {
			return reject(ctx.T("form.repeatuntil"))
		}

		if description != "" {
			description = strings.ReplaceAll(description, "\r", "")
			event.Props.SetText(ical.PropDescription, description)
		} else {
			event.Props.Del(ical.PropDescription)
		}

		// Naming anybody makes this an invitation: the account
		// becomes the organizer and everyone listed is asked
		// (RFC 5546). Removing the last one withdraws it.
		attendees := parseAttendees(ctx.FormValue("attendees"))
		had := attendeeLines(event) != ""
		setScheduling(event, ctx.Session.Username(), attendees)

		newID := uuid.New()
		if prop := event.Props.Get(ical.PropUID); prop == nil {
			event.Props.SetText(ical.PropUID, newID.String())
		}

		cal := newCalendar(event.Component)
		if !creating {
			cal = co.Data
		}
		ensureTimezones(cal, start)
		co, err = putObject(ctx, to, newID.String()+".ics", co, cal)
		if err != nil {
			return reject(fmt.Sprintf(ctx.T("form.saverefused"), err))
		}

		// The event is saved before anybody is told about it: a send
		// that fails must not lose what was written, so the failure
		// is reported and the meeting stays.
		method := alborzbase.MethodRequest
		told := attendees
		if len(attendees) == 0 && had {
			method, told = alborzbase.MethodCancel, parseAttendees(ctx.FormValue("attendees_was"))
		}
		if err := sendScheduling(ctx, event, told, method); err != nil {
			ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.T("invite.sendfailed")})
			ctx.Logger().Printf("failed to send the scheduling message: %v", err)
		} else if len(told) > 0 {
			ctx.PutNotice(ctx.T("invite.sent"))
		}

		return dav.Saved(ctx, creating, ctx.T("notice.eventcreated"), summary, CalendarObject{CalendarObject: co}.URL(), "/calendar", to.Account)
	}

	summary, _ := event.Props.Text("SUMMARY")

	rail, err := p.eventRail(ctx)
	if err != nil {
		return err
	}
	data := &UpdateEventRenderData{
		Rail:           rail,
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("title.update"), summary)),
		Groups:         groups,
		Calendar:       currentCalendar,
		CalendarObject: co,
		Event:          event,
	}
	fillEventForm(ctx, data, loc, newEventStart(ctx, loc))
	return ctx.Render(http.StatusOK, "update-event.html", data)
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

// exportVisible is the download a list page offers: what the rail shows
// as visible, across accounts, as one file.
func (p *plugin) exportVisible(ctx *alborz.Context, kind func([]string) bool, chosen func(*Settings) (bool, []string), comps []string, from, to time.Time, name string) error {
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return err
	}
	_, sites, err := visibleCalendars(accounts, ctx.URLAccount(), dav.Only(ctx, "cal"), kind, chosen)
	if err != nil {
		return err
	}
	var children []*ical.Component
	for _, r := range dav.Each(ctx.Request().Context(), sites, func(ctx context.Context, site dav.Site[*caldav.Client]) ([]*ical.Component, error) {
		return calendarComponents(ctx, site.Client, site.Collection.Path, comps, from, to)
	}) {
		if r.Err != nil {
			return r.Err
		}
		children = append(children, r.Value...)
	}
	body, err := encodeCalendar(children)
	if err != nil {
		return err
	}
	return dav.Download(ctx, name+".ics", body)
}

func (p *plugin) rawObject(ctx *alborz.Context) error {
	path, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}
	if !strings.Contains(path, "://") {
		return dav.Raw(ctx, p.client)
	}
	// An event of a feed has no object of its own on any server; it is
	// handed over as the calendar the page shows.
	uid := ctx.QueryParam("uid")
	_, cal, err := feedObject(ctx, path, uid)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return err
	}
	return dav.ServeRaw(ctx, uid+".ics", &buf)
}

// Tasks routes
// TaskList is the list page in one value: its rows in the order they
// are shown, the lists they came from, and what shaped them.
type TaskList struct {
	Rows      []TaskRow
	Calendars []dav.Collection
	// Items are the rows as the neighbours of a single task, in the
	// same order.
	Items   []dav.Item
	Query   string
	View    string
	Sorting dav.Sorting
}

// taskList is what the list page shows, in the order it shows it: the
// same visible lists, the same search, the same hidden completed ones
// and the same sort. A single task's page asks for it as well, to know
// what stands before and after it in the list it was opened from.
func (p *plugin) taskList(ctx *alborz.Context) (TaskList, error) {
	var rows []TaskRow
	loc := alborzbase.UserLocation(ctx)
	only := dav.Only(ctx, "cal")
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return TaskList{}, err
	}

	calendarInfos, sites, err := visibleCalendars(accounts, ctx.URLAccount(), only, supportsTodo, taskVisibility)
	if err != nil {
		return TaskList{}, err
	}
	search := ctx.QueryParam("query")
	// A star is a view, the way the mail rail's colours are: one
	// parameter, and the list is the search it names. Open tasks are
	// the list with no view; the completed ones and all of them are
	// views of their own.
	view := ctx.QueryParam("view")
	if view != viewCompleted && view != viewAll && view != viewHigh && !validStarView(view) {
		return TaskList{}, echo.NewHTTPError(http.StatusBadRequest, "no such view")
	}
	star := view
	withCompleted := view == viewCompleted || view == viewAll
	if withCompleted || view == viewHigh {
		star = ""
	}
	params := dav.ListParams(ctx, taskListParams...)

	query := taskQuery()

	for _, result := range dav.Each(ctx.Request().Context(), sites, func(ctx context.Context, site dav.Site[*caldav.Client]) ([]caldav.CalendarObject, error) {
		return site.Client.QueryCalendar(ctx, site.Collection.Path, &query)
	}) {
		if result.Err != nil {
			return TaskList{}, fmt.Errorf("failed to query tasks from %s: %v", result.Site.Collection.Name, result.Err)
		}

		for _, task := range result.Value {
			todo := getFirstTodo(task.Data)
			if todo == nil {
				continue
			}
			status, _ := todo.Props.Text("STATUS")
			// A server that ignores the STATUS filter sends every task;
			// the page hides what it was asked to hide either way.
			completed := status == "COMPLETED"
			if completed != (view == viewCompleted) && view != viewAll {
				continue
			}
			if !starMatches(componentColor(todo), star) {
				continue
			}
			if view == viewHigh && priorityBand(todo) != priorityHigh {
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
			rows = append(rows, taskRow(&task, result.Site.Collection, loc, params))
		}
	}

	sorting, err := dav.Sort(ctx, rows, taskColumns, func(row TaskRow) string {
		return strings.ToLower(row.Task.Account + "\x00" + row.Calendar.Name + "\x00" + row.Summary)
	}, "query")
	if err != nil {
		return TaskList{}, err
	}
	items := make([]dav.Item, len(rows))
	for i, row := range rows {
		items[i] = dav.Item{Path: row.Task.Path, URL: row.Href}
	}
	return TaskList{
		Rows:      rows,
		Items:     items,
		Calendars: calendarInfos,
		Query:     search,
		View:      view,
		Sorting:   sorting,
	}, nil
}

// taskColumns are the orders the task list can be put in, by summary
// unless asked.
var taskColumns = []dav.Column[TaskRow]{
	{Key: "summary", Value: func(row TaskRow) string { return strings.ToLower(row.Summary) }},
	{Key: "status", Value: func(row TaskRow) string {
		if row.Completed {
			return "1"
		}
		return "0"
	}},
	// High first; a task with none after the low ones.
	{Key: "priority", Value: func(row TaskRow) string {
		if i := slices.Index(priorityBands, row.Priority); i >= 0 {
			return strconv.Itoa(i)
		}
		return strconv.Itoa(len(priorityBands))
	}},
	{Key: alborzbase.ViewStarred, Value: func(row TaskRow) string { return dav.StarredFirst(row.Star) }},
	{Key: "account", Value: func(row TaskRow) string { return strings.ToLower(row.Task.Account) }},
	{Key: "calendar", Value: func(row TaskRow) string { return strings.ToLower(row.Calendar.Name) }},
	{Key: "due", Value: func(row TaskRow) string { return dav.When(row.Due) }},
	{Key: "added", Value: func(row TaskRow) string { return dav.When(row.Added) }},
}

func (p *plugin) tasks(ctx *alborz.Context) error {
	list, err := p.taskList(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "tasks.html", &TasksRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("title.tasks")),
		Quick:          quickListOf(ctx, list.Calendars),
		View:           list.View,
		Filters:        calendarFilters(ctx, list.Calendars),
		FilterRows:     taskRows(ctx, list.View),
		Calendars:      list.Calendars,
		Tasks:          list.Rows,
		Query:          list.Query,
		Sorting:        list.Sorting,
	})
}
func (p *plugin) task(ctx *alborz.Context) error {
	path, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	c, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
	if err != nil {
		return err
	}

	calendar := dav.Holding(calendars, "", path)
	if calendar == nil {
		if len(calendars) == 0 {
			return errNoCalendar
		}
		calendar = &calendars[0]
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
					"COLOR",
				},
			}},
		},
	}

	tasks, err := c.MultiGetCalendar(ctx.Request().Context(), path, &multiGet)
	if err != nil {
		return fmt.Errorf("failed to get task: %v", err)
	}
	if len(tasks) == 0 {
		return alborz.NotFound("notfound.task")
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

	rail, err := p.taskRail(ctx)
	if err != nil {
		return err
	}
	// The list the page was opened from is rebuilt to find what stands
	// either side; the DAV reads behind it are cached, so the cost is
	// the sorting, not another round trip.
	list, err := p.taskList(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "task.html", &TaskRenderData{
		Rail:           rail,
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(summary),
		Calendar:       calendar,
		Task:           TaskObject{CalendarObject: task},
		List:           dav.ListURL("/tasks", dav.ListParams(ctx, taskListParams...)),
		Star:           componentColor(getFirstTodo(task.Data)),
		Priority:       priorityBand(getFirstTodo(task.Data)),
		Neighbours:     dav.Around(list.Items, path),
	})
}
func (p *plugin) updateTask(ctx *alborz.Context) error {
	taskPath, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	loc := alborzbase.UserLocation(ctx)

	var c *caldav.Client
	var calendars []dav.Collection
	var groups []dav.Group
	var co *caldav.CalendarObject
	var todo *ical.Component
	var currentCalendar *dav.Collection
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
		currentCalendar = dav.Holding(calendars, "", co.Path)
	} else {
		groups, err = p.writableGroups(ctx, supportsTodo)
		if err != nil {
			return err
		}
		if len(groups) == 0 || len(groups[0].Collections) == 0 {
			return alborz.RenderInfo(ctx, http.StatusOK, ctx.T("calendar.nowritable"))
		}
		todo = ical.NewComponent(ical.CompToDo)
		todo.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
		currentCalendar = &groups[0].Collections[0]
	}

	if ctx.Request().Method == "POST" {
		summary := ctx.FormValue("summary")
		description := ctx.FormValue("description")
		dueDate := ctx.FormValue("due-date")
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		// A form without the select, the line above the list, says
		// nothing about priority and changes nothing.
		_, prioritySent := params["priority"]
		band := ctx.FormValue("priority")
		if band != "" && !slices.Contains(priorityBands, band) {
			return echo.NewHTTPError(http.StatusBadRequest, "no such priority")
		}

		reject := func(message string) error {
			rail, err := p.taskRail(ctx)
			if err != nil {
				return err
			}
			return ctx.Render(http.StatusUnprocessableEntity, "update-task.html", &UpdateTaskRenderData{
				Rail:           rail,
				BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("tasks.createtitle")),
				Groups:         groups,
				Calendar:       currentCalendar,
				CalendarObject: co,
				Todo:           todo,
				Due:            dueDate,
				Priority:       band,
				PriorityBands:  priorityBands,
				Error:          message,
			})
		}
		if summary == "" {
			return reject(ctx.T("form.summaryneeded"))
		}

		to := dav.Ref[*caldav.Client]{Client: c}
		creating := co == nil
		if creating {
			to, err = p.destination(ctx, ctx.FormValue("calendar"), supportsTodo)
			if errors.Is(err, dav.ErrNoDestination) {
				return reject(ctx.T("form.destinationneeded"))
			} else if err != nil {
				return err
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

		// The zone definition has to cover the date it qualifies,
		// so a dated task is bracketed by its own due date.
		due := time.Now().In(loc)
		if dueDate != "" {
			at, err := ctx.ReadDate(dueDate, loc)
			if err != nil {
				return reject(ctx.T("form.duedate"))
			}
			todo.Props.SetDateTime(ical.PropDue, at)
			due = at
		} else {
			todo.Props.Del(ical.PropDue)
		}
		if prioritySent {
			setPriorityBand(todo, band)
		}

		newID := uuid.New()
		if prop := todo.Props.Get(ical.PropUID); prop == nil {
			todo.Props.SetText(ical.PropUID, newID.String())
			todo.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
		}

		cal := newCalendar(todo)
		if !creating {
			cal = co.Data
		}
		ensureTimezones(cal, due)
		co, err = putObject(ctx, to, newID.String()+".ics", co, cal)
		if err != nil {
			return reject(fmt.Sprintf(ctx.T("form.saverefused"), err))
		}

		return dav.Saved(ctx, creating, ctx.T("notice.taskcreated"), summary, TaskObject{CalendarObject: co}.URL(), "/tasks", to.Account)
	}

	summary, _ := todo.Props.Text("SUMMARY")
	var due string
	if prop := todo.Props.Get(ical.PropDue); prop != nil {
		at, err := prop.DateTime(loc)
		if err != nil {
			return err
		}
		due = ctx.InputDate(at)
	}

	rail, err := p.taskRail(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "update-task.html", &UpdateTaskRenderData{
		Rail:           rail,
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("title.update"), summary)),
		Groups:         groups,
		Calendar:       currentCalendar,
		CalendarObject: co,
		Todo:           todo,
		Due:            due,
		Priority:       priorityBand(todo),
		PriorityBands:  priorityBands,
	})
}

// newCalendar is the object a new event or task is written as.
func newCalendar(comp *ical.Component) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, alborzbase.ItipProductID)
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Children = append(cal.Children, comp)
	return cal
}

// putObject writes a form's object where dav.Target says, on the
// condition it says.
func putObject(ctx *alborz.Context, to dav.Ref[*caldav.Client], name string, was *caldav.CalendarObject, cal *ical.Calendar) (*caldav.CalendarObject, error) {
	held, etag := "", ""
	if was != nil {
		held, etag = was.Path, was.ETag
	}
	at, ifMatch, ifNoneMatch := dav.Target(to.Path, name, held, etag)
	return to.Client.PutCalendarObject(ctx.Request().Context(), at, cal,
		&caldav.PutCalendarObjectOptions{IfMatch: ifMatch, IfNoneMatch: ifNoneMatch})
}

// eventList is the view an event's page came from: the day when the
// request names one, the month otherwise.
func eventList(ctx *alborz.Context) string {
	params := dav.ListParams(ctx, eventListParams...)
	if params.Get("date") != "" {
		return dav.ListURL("/calendar/date", params)
	}
	return dav.ListURL("/calendar", params)
}

// color marks events or tasks, from their own page or from their row:
// the star the mail and contact pages have, kept as COLOR (RFC 7986
// 5.9) on the component itself.
func (p *plugin) color(comp func(*ical.Calendar) *ical.Component, list string) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		return dav.Star(ctx, p.client, list, func(ctx *alborz.Context, ref dav.Ref[*caldav.Client], name string) (was string, err error) {
			_, err = changeComponent(ctx, ref, comp, func(target *ical.Component) {
				was = componentColor(target)
				setComponentColor(target, name)
			})
			return was, err
		})
	}
}

// complete marks tasks done or open again: the one a row or a page
// names, or the rows the list had checked.
func (p *plugin) complete(ctx *alborz.Context) error {
	done := wantsDone(ctx)
	words := func(ctx *alborz.Context, marked []dav.Ref[*caldav.Client], next string) alborz.Notice {
		if ctx.FormValue("undo") != "" {
			return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.T("notice.undone")}
		}
		paths := make([]string, len(marked))
		for i, ref := range marked {
			paths[i] = ref.Account + "|" + ref.Path
		}
		return completedNotice(ctx, done, len(marked), "/tasks/complete", url.Values{"paths": paths, "next": {next}})
	}
	var marked *caldav.CalendarObject
	return dav.Run(ctx, dav.Action[*caldav.Client]{
		Client: p.client,
		List:   "/tasks",
		Do: func(ctx *alborz.Context, ref dav.Ref[*caldav.Client]) (err error) {
			marked, err = changeComponent(ctx, ref, getFirstTodo, func(todo *ical.Component) { markTodo(todo, done) })
			return err
		},
		Done: words,
		Piece: func(ctx *alborz.Context, ref dav.Ref[*caldav.Client], next string) error {
			// The row the click was on is the whole of what changed, and
			// the same button undoes it, so a marked task answers with its
			// row and the list stays where it is - no notice, as a star's
			// does not.
			calendars, err := p.dav.Collections(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return err
			}
			holder := dav.Holding(calendars, "", ref.Path)
			if holder == nil {
				return errNoCalendar
			}
			// Only the pooled listing names the account on a calendar, and
			// the row's own links need it whichever page asked.
			cal := *holder
			cal.Account = ref.Account
			// The list's shape is in the address the form returns to; the
			// write's own URL says nothing about sort or search.
			row := taskRow(marked, cal, alborzbase.UserLocation(ctx), dav.ListParamsIn(next, taskListParams...))
			data := &TaskRowRenderData{BaseRenderData: *alborz.NewBaseRenderData(ctx), Row: row, Next: next}
			data.G = &data.BaseRenderData
			return ctx.Render(http.StatusOK, "task-row", data)
		},
	})
}

// TaskRowRenderData is one task's row, which is all that changes when
// it is marked from the list.
type TaskRowRenderData struct {
	alborz.BaseRenderData
	// G is what the row's own template asks the page for - the
	// translations and the globals - which a fragment has to hand it
	// by name, the list page being absent.
	G    *alborz.BaseRenderData
	Row  TaskRow
	Next string
}

// taskRow is one task as a row of the list. The list builds every row
// through it, and a write answers with the row it has just made rather
// than reading the collection again.
func taskRow(task *caldav.CalendarObject, cal dav.Collection, loc *time.Location, params url.Values) TaskRow {
	todo := getFirstTodo(task.Data)
	summary, _ := todo.Props.Text("SUMMARY")
	status, _ := todo.Props.Text("STATUS")
	// The raw property value is an iCal timestamp ("20260830T100000Z"),
	// which is not a thing to show anyone; parse it and let the page
	// write the date.
	due, _ := todo.Props.DateTime("DUE", loc)
	added, _ := todo.Props.DateTime("CREATED", loc)
	object := TaskObject{CalendarObject: task, Account: cal.Account}
	return TaskRow{
		Task:      object,
		Href:      dav.ObjectURL("/tasks/", task.Path, object.Account, params),
		Calendar:  cal,
		Summary:   summary,
		Status:    status,
		Due:       due,
		Added:     added,
		Completed: status == "COMPLETED",
		Priority:  priorityBand(todo),
		Star:      componentColor(todo),
	}
}

// wantsDone is the state a completion asks for: done, unless the form
// says reopen. An undo asks for the other one.
func wantsDone(ctx *alborz.Context) bool {
	return (ctx.FormValue("reopen") == "") != (ctx.FormValue("undo") != "")
}

// completedNotice counts the tasks marked, with the way back: the same
// form again, as an undo.
func completedNotice(ctx *alborz.Context, done bool, n int, action string, form url.Values) alborz.Notice {
	key := "notice.tasksdone"
	if !done {
		key = "notice.tasksopen"
		form.Set("reopen", "1")
	}
	return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.Tf(key, n), Action: ctx.Undo(action, form)}
}

func markTodo(todo *ical.Component, done bool) {
	if done {
		todo.Props.SetText(ical.PropStatus, "COMPLETED")
		todo.Props.SetDateTime(ical.PropCompleted, time.Now().UTC())
	} else {
		todo.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
		todo.Props.Del(ical.PropCompleted)
	}
}

// move copies each task into the chosen list and removes the original:
// the lists may belong to different accounts, and CalDAV MOVE does not
// cross servers.
func (p *plugin) move(ctx *alborz.Context) error {
	params, err := ctx.FormParams()
	if err != nil {
		return err
	}
	// A menu holds actions, one per row; the destination is a choice
	// the reader states on a page, which this same route answers with.
	to := params.Get("to")
	if to == "" {
		list, err := p.taskList(ctx)
		if err != nil {
			return err
		}
		rail, err := p.taskRail(ctx)
		if err != nil {
			return err
		}
		return ctx.Render(http.StatusOK, "move-task.html", &MoveTasksRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("folder.moveask")),
			Rail:           rail,
			Calendars:      list.Calendars,
			Paths:          params["paths"],
			Next:           ctx.NextOr(ctx.AccountPath("/tasks")),
		})
	}
	targets, err := dav.Selected(ctx, []string{to}, p.client)
	if err != nil {
		return err
	}
	target := targets[0]
	return dav.Run(ctx, dav.Action[*caldav.Client]{Client: p.client, List: "/tasks",
		Do: func(ctx *alborz.Context, ref dav.Ref[*caldav.Client]) error {
			if path.Dir(ref.Path)+"/" == target.Path {
				return nil
			}
			co, err := getCalendarObject(ctx, ref.Client, ref.Path)
			if err != nil {
				return fmt.Errorf("failed to get task: %v", err)
			}
			if _, err := target.Client.PutCalendarObject(ctx.Request().Context(), target.Path+path.Base(ref.Path), co.Data,
				&caldav.PutCalendarObjectOptions{IfNoneMatch: dav.IfNew}); err != nil {
				return err
			}
			return dav.Delete(ctx, ref)
		}})
}

// joinCalendars makes one calendar of the objects' components.
func joinCalendars(objects [][]byte) ([]byte, error) {
	var children []*ical.Component
	for _, object := range objects {
		cal, err := ical.NewDecoder(bytes.NewReader(object)).Decode()
		if err != nil {
			return nil, err
		}
		children = append(children, cal.Children...)
	}
	return encodeCalendar(children)
}

// taskQuery is what the task list asks a list for: every task.
//
// The open tasks would be two queries, since an open task may carry no
// STATUS and CalDAV filters have no OR: STATUS is-not-defined, and
// STATUS not COMPLETED. go-webdav's client (v0.7.0) drops
// is-not-defined as it encodes a filter, which turns the first into
// "STATUS is defined" and loses every task without one - what phones
// and other clients commonly write. Until it sends the element, the
// open tasks are every task, and the page hides the completed.
func taskQuery() caldav.CalendarQuery {
	return caldav.CalendarQuery{
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
}

// warm fetches what the calendar and task pages ask for first, as the
// account is signed in, so the first click on either finds it cached:
// the calendars, this month's events and the open tasks.
func (p *plugin) warm(ctx *alborz.Context, s *alborz.Session) {
	loc := alborzbase.UserLocation(ctx)
	p.dav.Warm(ctx, s, func(bg context.Context) error { return p.warmAccount(bg, s, loc) })
}

func (p *plugin) warmAccount(ctx context.Context, s *alborz.Session, loc *time.Location) error {
	c, calendars, err := p.clientWithCalendars(ctx, s)
	if err != nil {
		return err
	}
	// The month page's range for the month the reader is in.
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	events := eventQuery(start.AddDate(0, 0, -7), start.AddDate(0, 1, 0).AddDate(0, 0, 7))
	tasks := taskQuery()
	type ask struct {
		path  string
		query caldav.CalendarQuery
	}
	var asks []ask
	for _, cal := range calendars {
		if supportsEvent(cal.Components) {
			asks = append(asks, ask{cal.Path, events})
		}
		if supportsTodo(cal.Components) {
			asks = append(asks, ask{cal.Path, tasks})
		}
	}
	for _, r := range dav.Each(ctx, asks, func(ctx context.Context, a ask) (int, error) {
		_, err := c.QueryCalendar(ctx, a.path, &a.query)
		return 0, err
	}) {
		if r.Err != nil {
			return r.Err
		}
	}
	return nil
}
