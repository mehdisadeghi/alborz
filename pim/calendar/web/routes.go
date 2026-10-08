package web

import (
	"context"
	"fmt"
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
	return p.calendarRail(ctx, calendar.SupportsEvent, calendar.EventVisibility, "calendar.month", "", "calendar.newcalendar", "calendar.import", "calendar.import", true)
}

func (p *plugin) taskRail(ctx *web.Context) (pimweb.Rail, error) {
	return p.calendarRail(ctx, calendar.SupportsTodo, calendar.TaskVisibility, "calendar.tasks", "tasks", "tasks.newlist", "calendar.tasks.import", "tasks.import", false)
}

func (p *plugin) journalRail(ctx *web.Context) (pimweb.Rail, error) {
	return p.calendarRail(ctx, calendar.SupportsJournal, calendar.JournalVisibility, "calendar.journal", "journal", "journal.newjournal", "calendar.journal.import", "journal.import", false)
}

func (p *plugin) calendarRail(ctx *web.Context, kind func([]string) bool, chosen func(*calendar.Settings) (*bool, *[]string), list, forParam, newLabel, importRoute, importLabel string, follow bool) (pimweb.Rail, error) {
	rail := pimweb.Rail{Field: "cal", ItemClass: "calendar-item", Edit: "calendar.collection"}
	next := web.QueryValue(ctx.Request().URL.RequestURI())
	rail.Path, rail.Action = ctx.Path(list), ctx.Path(list)
	rail.NewHref = ctx.Path("calendar.calendars.create") + "?"
	if forParam != "" {
		rail.NewHref += "for=" + forParam + "&"
	}
	rail.NewHref += "next=" + next
	rail.NewLabel = ctx.T(newLabel)
	rail.ImportHref, rail.ImportLabel = ctx.Path(importRoute), ctx.T(importLabel)
	if follow {
		rail.FollowHref, rail.FollowLabel = ctx.Path("calendar.subscribe")+"?next="+next, ctx.T("calendar.subscribe")
	}
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

// section is the events, the tasks or the journal: the routes of their list and of one of them.
type section struct{ list, object string }

var (
	eventSection   = section{"calendar.month", "calendar.event"}
	taskSection    = section{"calendar.tasks", "calendar.task"}
	journalSection = section{"calendar.journal", "calendar.entry"}
)

func registerRoutes(p *plugin, g *web.Group) {
	guard := func(h func(*web.Context) error) func(*web.Context) error {
		return pimweb.Guarded(calendar.ErrNoCalendar, "calendar.calendars.create", h)
	}
	GET, POST, Page, download := pimweb.GuardedRoutes(g, guard)
	upload := pimweb.GuardedUpload(g, guard)
	POST("month", "/calendar", pimweb.HandleChoose(p.DAV, "calendar.month", "cal", calendar.Choose(calendar.EventVisibility)))
	POST("tasks", "/tasks", pimweb.HandleChoose(p.DAV, "calendar.tasks", "cal", calendar.Choose(calendar.TaskVisibility)))
	POST("refresh", "/calendar/refresh", p.refresh)
	POST("tasks.refresh", "/tasks/refresh", pimweb.HandleRefresh(p.DAV, "calendar.tasks"))
	POST("journal", "/journal", pimweb.HandleChoose(p.DAV, "calendar.journal", "cal", calendar.Choose(calendar.JournalVisibility)))
	POST("journal.refresh", "/journal/refresh", pimweb.HandleRefresh(p.DAV, "calendar.journal"))
	GET("month", "/calendar", p.month)
	download("month.export", "/calendar/export", p.exportMonth)
	GET("day", "/calendar/date", p.day)
	GET("search", "/calendar/search", p.search)
	GET("event", "/calendar/{path}", p.event)

	download("event.raw", "/calendar/{path}/raw", p.rawObject)
	download("task.raw", "/tasks/{path}/raw", p.rawObject)
	download("entry.raw", "/journal/{path}/raw", p.rawObject)
	Page("subscribe", "/calendars/subscribe", handleSubscribe(p))
	page := p.collectionPage()
	Page("calendars.create", "/calendars/create", page.HandleCreate(p.DAV, p.createForm))
	calendarImport, calendarOver := page.HandleImportPage(p.DAV, "calendar.month", "nav.calendar", "calendar.import", "calendar.importhint", "event", true)
	GET("import", "/calendar/import", calendarImport)
	upload("import", "/calendar/import", pimweb.ImportBodySize, calendarImport, calendarOver)
	tasksImport, tasksOver := page.HandleImportPage(p.DAV, "calendar.tasks", "nav.tasks", "tasks.import", "tasks.importhint", "task", false)
	GET("tasks.import", "/tasks/import", tasksImport)
	upload("tasks.import", "/tasks/import", pimweb.ImportBodySize, tasksImport, tasksOver)
	journalImport, journalOver := page.HandleImportPage(p.DAV, "calendar.journal", "nav.journal", "journal.import", "journal.importhint", "entry", false)
	GET("journal.import", "/journal/import", journalImport)
	upload("journal.import", "/journal/import", pimweb.ImportBodySize, journalImport, journalOver)
	for _, method := range []func(string, string, func(*web.Context) error){GET, POST} {
		method("exportall", "/calendar/export-all", page.HandleExportAll("calendar.month", "calendars.zip"))
		method("tasks.exportall", "/tasks/export-all", page.HandleExportAll("calendar.tasks", "task-lists.zip"))
		method("journal.exportall", "/journal/export-all", page.HandleExportAll("calendar.journal", "journals.zip"))
	}
	Page("collection", "/calendars/{path}", page.Handle(p.DAV))
	POST("collection.delete", "/calendars/{path}/delete", page.HandleDelete(p.DAV))
	upload("collection.import", "/calendars/{path}/import", pimweb.ImportBodySize, page.HandleImport, page.HandleImportTooLarge)
	download("collection.export", "/calendars/{path}/export", page.HandleExport)
	POST("collection.share", "/calendars/{path}/share", page.HandleShare(p.DAV))
	POST("collection.unshare", "/calendars/{path}/unshare", page.HandleUnshare(p.DAV))
	POST("collection.access", "/calendars/{path}/access", page.HandleAccess(p.DAV))
	POST("collection.accept", "/calendars/{path}/accept", page.HandleAnswer(p.DAV, true))
	POST("collection.decline", "/calendars/{path}/decline", page.HandleAnswer(p.DAV, false))
	POST("collection.publish", "/calendars/{path}/publish", page.HandlePublish(p.DAV, true))
	POST("collection.unpublish", "/calendars/{path}/unpublish", page.HandlePublish(p.DAV, false))
	Page("event.create", "/calendar/create", p.updateEvent)
	Page("event.update", "/calendar/{path}/update", p.updateEvent)
	remove := func(s section) func(*web.Context) error {
		if s == eventSection {
			return pimweb.NoticeHandler(p.Client, s.list, "notice.eventsdeleted", p.deleteEvent)
		}
		key := "notice.tasksdeleted"
		if s == journalSection {
			key = "notice.entriesdeleted"
		}
		return pimweb.NoticeHandler(p.Client, s.list, key, pimweb.Delete[*caldav.Client])
	}
	POST("events.delete", "/calendar/delete", remove(eventSection))
	POST("event.delete", "/calendar/{path}/delete", remove(eventSection))
	Page("event.copy", "/calendar/{path}/copy", p.copyTo(eventSection))
	GET("tasks", "/tasks", p.tasks)
	GET("task", "/tasks/{path}", p.task)

	Page("task.create", "/tasks/create", p.updateTask)
	Page("task.edit", "/tasks/{path}/edit", p.updateTask)
	POST("task.delete", "/tasks/{path}/delete", remove(taskSection))
	POST("tasks.delete", "/tasks/delete", remove(taskSection))
	Page("task.copy", "/tasks/{path}/copy", p.copyTo(taskSection))
	POST("tasks.complete", "/tasks/complete", p.complete)
	GET("tasks.move", "/tasks/move", p.move)
	POST("tasks.move", "/tasks/move", p.move)
	POST("tasks.export", "/tasks/export", pimweb.HandleExport(p.Client, "calendar.tasks",
		func(ctx *web.Context) string { return ctx.T("nav.tasks") + ".ics" }, calendar.Join))

	GET("journal", "/journal", p.journal)
	GET("entry", "/journal/{path}", p.entry)
	Page("entry.create", "/journal/create", p.updateEntry)
	Page("entry.edit", "/journal/{path}/edit", p.updateEntry)
	POST("entry.delete", "/journal/{path}/delete", remove(journalSection))
	POST("journal.delete", "/journal/delete", remove(journalSection))
	Page("entry.copy", "/journal/{path}/copy", p.copyTo(journalSection))
	POST("journal.export", "/journal/export", pimweb.HandleExport(p.Client, "calendar.journal",
		func(ctx *web.Context) string { return ctx.T("nav.journal") + ".ics" }, calendar.Join))
	POST("entry.color", "/journal/{path}/color", p.color(calendar.FirstJournal, journalSection))

	POST("task.note", "/tasks/{path}/note", p.note(calendar.FirstTodo, taskSection))
	POST("event.note", "/calendar/{path}/note", p.note(calendar.FirstEvent, eventSection))
	POST("task.complete", "/tasks/{path}/complete", p.complete)
	POST("task.color", "/tasks/{path}/color", p.color(calendar.FirstTodo, taskSection))
	POST("event.color", "/calendar/{path}/color", p.color(calendar.FirstEvent, eventSection))
}

// Appended and dated rather than replacing: a note people use is a record.
func (p *plugin) note(comp func(*ical.Calendar) *ical.Component, s section) func(*web.Context) error {
	return func(ctx *web.Context) error {
		return pimweb.Note(ctx, p.Client, s.list, func(ctx *web.Context, ref pim.Ref[*caldav.Client], note string) error {
			_, err := calendar.ChangeComponent(ctx.Request().Context(), ref, comp, func(target *ical.Component) {
				existing, _ := target.Props.Text(ical.PropDescription)
				target.Props.SetText(ical.PropDescription, pim.AppendNote(existing, note, time.Now().In(web.UserLocation(ctx))))
			})
			return err
		})
	}
}

func calendarFilters(ctx *web.Context) []web.Filter {
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

func (p *plugin) exportVisible(ctx *web.Context, kind func([]string) bool, chosen func(*calendar.Settings) (*bool, *[]string), comps []string, from, to time.Time, name string) error {
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return err
	}
	_, sites, err := calendar.Visible(ctx.Request().Context(), accounts, ctx.URLAccount(), pimweb.Only(ctx, "cal"), kind, chosen)
	if err != nil {
		return err
	}
	type read struct {
		children []*ical.Component
		left     []error
	}
	var children []*ical.Component
	var left []error
	for _, r := range pim.Present(p.DAV, dav.Across(ctx.Request().Context(), sites, pim.Site[*caldav.Client].Account, func(ctx context.Context, site pim.Site[*caldav.Client]) (read, error) {
		children, left, err := calendar.Components(ctx, site.Client, site.Collection.Path, comps, from, to)
		return read{children, left}, err
	})) {
		// A calendar that does not answer is left out with its reason, as an object that does not parse is.
		if r.Err != nil {
			left = append(left, fmt.Errorf("%s: %w", r.Site.Collection.Name, r.Err))
			continue
		}
		children = append(children, r.Value.children...)
		for _, err := range r.Value.left {
			left = append(left, fmt.Errorf("%s: %w", r.Site.Collection.Name, err))
		}
	}
	// A VCALENDAR holds one component at least (RFC 5545 3.6).
	if len(children) == 0 && len(left) == 0 {
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("calendar.noevents")})
		page := *ctx.Request().URL
		page.Path = ctx.Path("calendar.month")
		return ctx.FoundRedirect(page.RequestURI())
	}
	body, err := calendar.Encode(children, left)
	if err := pimweb.LeftOut(ctx, err); err != nil {
		return err
	}
	if len(body) == 0 {
		page := *ctx.Request().URL
		page.Path = ctx.Path("calendar.month")
		return ctx.FoundRedirect(page.RequestURI())
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
