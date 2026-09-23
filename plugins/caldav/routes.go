package alborzcaldav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

type Settings struct {
	CalendarFilter   bool
	VisibleCalendars []string
	TaskFilter       bool
	VisibleTasks     []string
	Subscriptions    []Subscription
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
	next := alborz.QueryValue(ctx.Request().URL.RequestURI())
	rail.Path, rail.Action = "/calendar", "/calendar"
	rail.NewHref, rail.NewLabel = "/calendars/create?next="+next, ctx.T("calendar.newcalendar")
	rail.FollowHref, rail.FollowLabel = "/calendars/subscribe?next="+next, ctx.T("calendar.subscribe")
	rail.ImportHref, rail.ImportLabel = "/calendar/import", ctx.T("calendar.import")
	return rail, err
}

func (p *plugin) taskRail(ctx *alborz.Context) (dav.Rail, error) {
	rail, err := p.calendarRail(ctx, supportsTodo, taskVisibility)
	rail.Path, rail.Action = "/tasks", "/tasks"
	rail.NewHref, rail.NewLabel = "/calendars/create?for=tasks&next="+alborz.QueryValue(ctx.Request().URL.RequestURI()), ctx.T("tasks.newlist")
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
		method("/calendar/export-all", page.HandleExportAll("/calendar", "calendars.zip"))
		method("/tasks/export-all", page.HandleExportAll("/tasks", "task-lists.zip"))
		method("/tasks/import", page.HandleImportPage(p.dav, "/tasks", "nav.tasks", "tasks.import", "tasks.importhint", "task", false))
	}
	GET("/calendars/:path", page.Handle(p.dav))
	POST("/calendars/:path", p.forSubscription(p.updateSubscription, page.Handle(p.dav)))
	POST("/calendars/:path/delete", p.forSubscription(p.unsubscribe, page.HandleDelete(p.dav)))
	POST("/calendars/:path/import", page.HandleImport(p.dav))
	GET("/calendars/:path/export", page.HandleExport(p.dav))
	POST("/calendars/:path/share", page.HandleShare(p.dav))
	POST("/calendars/:path/unshare", page.HandleUnshare(p.dav))
	POST("/calendars/:path/access", page.HandleAccess(p.dav))
	POST("/calendars/:path/accept", page.HandleAnswer(p.dav, true))
	POST("/calendars/:path/decline", page.HandleAnswer(p.dav, false))
	POST("/calendars/:path/publish", page.HandlePublish(p.dav, true))
	POST("/calendars/:path/unpublish", page.HandlePublish(p.dav, false))
	p.Inject("*", p.dav.InjectInvitations("cal", page.Base, "/calendar", "/calendars", "/tasks"))
	p.Inject("message.html", page.InjectOffer(p.dav))
	GET("/calendar/create", p.updateEvent)
	POST("/calendar/create", p.updateEvent)
	GET("/calendar/:path/update", p.updateEvent)
	POST("/calendar/:path/update", p.updateEvent)
	remove := func(list string) func(*alborz.Context) error {
		key := "notice.eventsdeleted"
		if list == "/tasks" {
			key = "notice.tasksdeleted"
		}
		return dav.Handler(dav.Action[*caldav.Client]{Client: p.client, Do: dav.Delete[*caldav.Client], List: list,
			Done: func(ctx *alborz.Context, done []dav.Ref[*caldav.Client], _ string) alborz.Notice {
				return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.Tf(key, len(done))}
			}})
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
		return dav.Run(ctx, dav.Action[*caldav.Client]{Client: p.client, List: list, Done: dav.Quiet[*caldav.Client],
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
	// The calendar the page is in is a place, not a narrowing of one:
	// the rail marks it and the crumb names it, as a mail folder is
	// named. A chip would say it a third time, and its cross would
	// navigate rather than widen.
	return out
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
