package web

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

const datePageLayout = "2006-01-02"

func (p *plugin) eventRail(ctx *web.Context) (pimweb.Rail, error) {
	rail, err := p.calendarRail(ctx, calendar.SupportsEvent, calendar.EventVisibility)
	next := web.QueryValue(ctx.Request().URL.RequestURI())
	rail.Path, rail.Action = ctx.Path("calendar.month"), ctx.Path("calendar.month")
	rail.NewHref, rail.NewLabel = ctx.Path("calendar.calendars.create")+"?next="+next, ctx.T("calendar.newcalendar")
	rail.FollowHref, rail.FollowLabel = ctx.Path("calendar.subscribe")+"?next="+next, ctx.T("calendar.subscribe")
	rail.ImportHref, rail.ImportLabel = ctx.Path("calendar.import"), ctx.T("calendar.import")
	return rail, err
}

func (p *plugin) taskRail(ctx *web.Context) (pimweb.Rail, error) {
	rail, err := p.calendarRail(ctx, calendar.SupportsTodo, calendar.TaskVisibility)
	rail.Path, rail.Action = ctx.Path("calendar.tasks"), ctx.Path("calendar.tasks")
	rail.NewHref, rail.NewLabel = ctx.Path("calendar.calendars.create")+"?for=tasks&next="+web.QueryValue(ctx.Request().URL.RequestURI()), ctx.T("tasks.newlist")
	rail.ImportHref, rail.ImportLabel = ctx.Path("calendar.tasks.import"), ctx.T("tasks.import")
	return rail, err
}

func (p *plugin) calendarRail(ctx *web.Context, kind func([]string) bool, chosen func(*calendar.Settings) (bool, []string)) (pimweb.Rail, error) {
	rail := pimweb.Rail{Field: "cal", ItemClass: "calendar-item", Edit: "calendar.collection"}
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return rail, err
	}
	if rail.Invitations, err = p.invitations(ctx); err != nil {
		return rail, err
	}
	infos, _, err := calendar.Visible(ctx.Request().Context(), accounts, ctx.URLAccount(), nil, kind, chosen)
	rail.Items = infos
	return rail, err
}

// section is the events or the tasks: the routes of their list and of one of them.
type section struct{ list, object string }

var (
	eventSection = section{"calendar.month", "calendar.event"}
	taskSection  = section{"calendar.tasks", "calendar.task"}
)

func registerRoutes(p *plugin, g *web.Group) {
	guard := func(h func(*web.Context) error) func(*web.Context) error {
		return pimweb.Guarded(p.DAV, calendar.ErrNoCalendar, "calendar.calendars.create", h)
	}
	GET := func(name, path string, h func(*web.Context) error) { g.GET(name, path, guard(h)) }
	POST := func(name, path string, h func(*web.Context) error) { g.POST(name, path, guard(h)) }
	download := func(name, path string, h func(*web.Context) error) { g.Download(name, path, guard(h)) }
	upload := func(name, path string, h, over func(*web.Context) error) {
		g.Upload(name, path, pimweb.ImportBodySize, guard(h), guard(over))
	}
	POST("month", "/calendar", pimweb.HandleChoose("calendar.month", "cal", calendar.Choose(func(s *calendar.Settings, paths []string) {
		s.CalendarFilter, s.VisibleCalendars = true, paths
	})))
	POST("tasks", "/tasks", pimweb.HandleChoose("calendar.tasks", "cal", calendar.Choose(func(s *calendar.Settings, paths []string) {
		s.TaskFilter, s.VisibleTasks = true, paths
	})))
	POST("refresh", "/calendar/refresh", p.refresh)
	POST("tasks.refresh", "/tasks/refresh", pimweb.HandleRefresh(p.DAV, "calendar.tasks"))
	GET("month", "/calendar", p.month)
	download("month.export", "/calendar/export", p.exportMonth)
	GET("day", "/calendar/date", p.day)
	GET("event", "/calendar/{path}", p.event)

	download("event.raw", "/calendar/{path}/raw", p.rawObject)
	download("task.raw", "/tasks/{path}/raw", p.rawObject)
	GET("subscribe", "/calendars/subscribe", handleSubscribe(p))
	POST("subscribe", "/calendars/subscribe", handleSubscribe(p))
	page := p.collectionPage()
	GET("calendars.create", "/calendars/create", page.HandleCreate(p.DAV, p.createForm))
	POST("calendars.create", "/calendars/create", page.HandleCreate(p.DAV, p.createForm))
	calendarImport, calendarOver := page.HandleImportPage(p.DAV, "calendar.month", "nav.calendar", "calendar.import", "calendar.importhint", "event", true)
	GET("import", "/calendar/import", calendarImport)
	upload("import", "/calendar/import", calendarImport, calendarOver)
	tasksImport, tasksOver := page.HandleImportPage(p.DAV, "calendar.tasks", "nav.tasks", "tasks.import", "tasks.importhint", "task", false)
	GET("tasks.import", "/tasks/import", tasksImport)
	upload("tasks.import", "/tasks/import", tasksImport, tasksOver)
	for _, method := range []func(string, string, func(*web.Context) error){GET, POST} {
		method("exportall", "/calendar/export-all", page.HandleExportAll("calendar.month", "calendars.zip"))
		method("tasks.exportall", "/tasks/export-all", page.HandleExportAll("calendar.tasks", "task-lists.zip"))
	}
	GET("collection", "/calendars/{path}", page.Handle(p.DAV))
	POST("collection", "/calendars/{path}", page.Handle(p.DAV))
	POST("collection.delete", "/calendars/{path}/delete", page.HandleDelete(p.DAV))
	upload("collection.import", "/calendars/{path}/import", page.HandleImport(p.DAV), page.HandleImportTooLarge)
	download("collection.export", "/calendars/{path}/export", page.HandleExport(p.DAV))
	POST("collection.share", "/calendars/{path}/share", page.HandleShare(p.DAV))
	POST("collection.unshare", "/calendars/{path}/unshare", page.HandleUnshare(p.DAV))
	POST("collection.access", "/calendars/{path}/access", page.HandleAccess(p.DAV))
	POST("collection.accept", "/calendars/{path}/accept", page.HandleAnswer(p.DAV, true))
	POST("collection.decline", "/calendars/{path}/decline", page.HandleAnswer(p.DAV, false))
	POST("collection.publish", "/calendars/{path}/publish", page.HandlePublish(p.DAV, true))
	POST("collection.unpublish", "/calendars/{path}/unpublish", page.HandlePublish(p.DAV, false))
	GET("event.create", "/calendar/create", p.updateEvent)
	POST("event.create", "/calendar/create", p.updateEvent)
	GET("event.update", "/calendar/{path}/update", p.updateEvent)
	POST("event.update", "/calendar/{path}/update", p.updateEvent)
	remove := func(s section) func(*web.Context) error {
		key, do := "notice.eventsdeleted", p.deleteEvent
		if s == taskSection {
			key, do = "notice.tasksdeleted", pimweb.Delete[*caldav.Client]
		}
		return pimweb.Handler(pimweb.Action[*caldav.Client]{Client: p.Client, Do: do, List: s.list,
			Done: func(ctx *web.Context, done []pim.Ref[*caldav.Client], _ string) web.Notice {
				return web.Notice{Kind: web.NoticeDone, Text: ctx.Tf(key, len(done))}
			}})
	}
	POST("events.delete", "/calendar/delete", remove(eventSection))
	POST("event.delete", "/calendar/{path}/delete", remove(eventSection))
	GET("event.copy", "/calendar/{path}/copy", p.copyTo(eventSection))
	POST("event.copy", "/calendar/{path}/copy", p.copyTo(eventSection))
	GET("tasks", "/tasks", p.tasks)
	GET("task", "/tasks/{path}", p.task)

	GET("task.create", "/tasks/create", p.updateTask)
	POST("task.create", "/tasks/create", p.updateTask)
	GET("task.edit", "/tasks/{path}/edit", p.updateTask)
	POST("task.edit", "/tasks/{path}/edit", p.updateTask)
	POST("task.delete", "/tasks/{path}/delete", remove(taskSection))
	POST("tasks.delete", "/tasks/delete", remove(taskSection))
	GET("task.copy", "/tasks/{path}/copy", p.copyTo(taskSection))
	POST("task.copy", "/tasks/{path}/copy", p.copyTo(taskSection))
	POST("tasks.complete", "/tasks/complete", p.complete)
	GET("tasks.move", "/tasks/move", p.move)
	POST("tasks.move", "/tasks/move", p.move)
	POST("tasks.export", "/tasks/export", pimweb.HandleExport(p.Client, "calendar.tasks",
		func(ctx *web.Context) string { return ctx.T("nav.tasks") + ".ics" }, calendar.Join))

	POST("task.note", "/tasks/{path}/note", p.note(calendar.FirstTodo, taskSection))
	POST("event.note", "/calendar/{path}/note", p.note(calendar.FirstEvent, eventSection))
	POST("task.complete", "/tasks/{path}/complete", p.complete)
	POST("task.color", "/tasks/{path}/color", p.color(calendar.FirstTodo, taskSection))
	POST("event.color", "/calendar/{path}/color", p.color(calendar.FirstEvent, eventSection))
}

// Appended and dated rather than replacing: a note people use is a record.
func (p *plugin) note(comp func(*ical.Calendar) *ical.Component, s section) func(*web.Context) error {
	return func(ctx *web.Context) error {
		note := strings.TrimSpace(ctx.FormValue("note"))
		return pimweb.Run(ctx, pimweb.Action[*caldav.Client]{Client: p.Client, List: s.list, Done: pimweb.Quiet[*caldav.Client],
			Do: func(ctx *web.Context, ref pim.Ref[*caldav.Client]) error {
				if note == "" {
					return nil
				}
				_, err := calendar.ChangeComponent(ctx.Request().Context(), ref, comp, func(target *ical.Component) {
					existing, _ := target.Props.Text(ical.PropDescription)
					target.Props.SetText(ical.PropDescription, pim.AppendNote(existing, note, time.Now().In(web.UserLocation(ctx))))
				})
				return err
			}})
	}
}

func calendarFilters(ctx *web.Context, calendars []dav.Collection) []web.Filter {
	var out []web.Filter
	if f, ok := ctx.FilterOn("view", ctx.T("filter.color")); ok && f.Value != "list" {
		switch f.Value {
		case viewCompleted:
			f.Label, f.Value = ctx.T("tasks.status"), ctx.T("tasks.completed")
		case viewAll:
			f.Label, f.Value = ctx.T("tasks.status"), ctx.T("tasks.all")
		case viewHigh:
			f.Label, f.Value = ctx.T("tasks.priority"), ctx.T("tasks.priority.high")
		case alborz.ViewStarred:
			f.Value = ctx.T("mailbox.starred")
		default:
			f.Value = ctx.T("color." + f.Value)
		}
		// Cleared of its star the agenda is still the agenda, not the grid.
		if ctx.Request().URL.Path == ctx.Path("calendar.month") {
			f.Href = ctx.WithParam("view", "list")
		}
		out = append(out, f)
	}
	// The calendar the page is in is a place, named by rail and crumb, not a chip.
	return out
}

func (p *plugin) exportVisible(ctx *web.Context, kind func([]string) bool, chosen func(*calendar.Settings) (bool, []string), comps []string, from, to time.Time, name string) error {
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return err
	}
	_, sites, err := calendar.Visible(ctx.Request().Context(), accounts, ctx.URLAccount(), pimweb.Only(ctx, "cal"), kind, chosen)
	if err != nil {
		return err
	}
	var children []*ical.Component
	for _, r := range pim.Present(p.DAV, dav.Across(ctx.Request().Context(), sites, pim.Site[*caldav.Client].Account, func(ctx context.Context, site pim.Site[*caldav.Client]) ([]*ical.Component, error) {
		return calendar.Components(ctx, site.Client, site.Collection.Path, comps, from, to)
	})) {
		if r.Err != nil {
			return r.Err
		}
		children = append(children, r.Value...)
	}
	// A VCALENDAR holds one component at least (RFC 5545 3.6).
	if len(children) == 0 {
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("calendar.noevents")})
		page := *ctx.Request().URL
		page.Path = ctx.Path("calendar.month")
		return ctx.Redirect(http.StatusFound, page.RequestURI())
	}
	body, err := calendar.Encode(children)
	if err != nil {
		return err
	}
	return pimweb.Download(ctx, name+".ics", body)
}

func (p *plugin) rawObject(ctx *web.Context) error {
	return pimweb.Raw(ctx, p.Client)
}

func (p *plugin) color(comp func(*ical.Calendar) *ical.Component, s section) func(*web.Context) error {
	return func(ctx *web.Context) error {
		return pimweb.Star(ctx, p.Client, s.list, s.object, func(ctx *web.Context, ref pim.Ref[*caldav.Client], name string) (was string, err error) {
			_, err = calendar.ChangeComponent(ctx.Request().Context(), ref, comp, func(target *ical.Component) {
				was = calendar.ComponentColor(target)
				calendar.SetComponentColor(target, name)
			})
			return was, err
		})
	}
}

func (p *plugin) warm(ctx *web.Context, s *alborz.Login) {
	loc := web.UserLocation(ctx)
	p.DAV.Warm(s, func(bg context.Context) error { return p.WarmAccount(bg, s, loc) })
}

func validStarView(view string) bool {
	return view == "" || view == alborz.ViewStarred || slices.Contains(alborz.FlagColors[:], view)
}

// pooledCalendars lists every account's calendars.
func (p *plugin) pooledCalendars(ctx *web.Context) ([]pim.Account[*caldav.Client], error) {
	return pimweb.Pooled(ctx, p.DAV, p.OpenWithFeeds, calendar.ErrNoCalendar)
}

func (p *plugin) writableGroups(ctx *web.Context, supports func([]string) bool) ([]dav.Group, error) {
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return nil, err
	}
	return pim.WritableGroups(accounts, func(cal dav.Collection) bool { return cal.Writable && supports(cal.Components) }), nil
}

func (p *plugin) destination(ctx *web.Context, value string, supports func([]string) bool) (pim.Ref[*caldav.Client], error) {
	return pimweb.Destination(ctx, value, p.Open, func(cal dav.Collection) bool {
		return cal.Writable && supports(cal.Components)
	})
}

// refresh fetches every signed-in account's feeds, then asks the DAV servers again.
func (p *plugin) refresh(ctx *web.Context) error {
	if err := p.RefreshFeeds(ctx.Request().Context(), ctx.Logins()); err != nil {
		return err
	}
	return pimweb.HandleRefresh(p.DAV, "calendar.month")(ctx)
}
