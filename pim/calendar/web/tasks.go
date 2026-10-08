package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

// DestinationRenderData asks where a move or a copy goes; a menu holds actions, so the choice is a page.
type DestinationRenderData struct {
	web.PageData[pimweb.Rail]
	Section, Home string
	Title, Lead   string
	Label, Save   string
	// Action is where the form posts; empty posts to the page's own address.
	Action string
	// Key is the picker's remembered choice.
	Key    string
	Groups []dav.Group
	Paths  []string
	Next   string
}

func destinationRenderData(ctx *web.Context, rail pimweb.Rail, section, home, title, lead, label, save, key string, groups []dav.Group, next string) *DestinationRenderData {
	return &DestinationRenderData{
		PageData: *web.NewPageData(ctx, title, rail), Section: section, Home: home, Title: title,
		Lead: lead, Label: label, Save: save, Key: key, Groups: groups, Next: next,
	}
}

type TasksRenderData struct {
	pimweb.ListRenderData
	Tasks []TaskRow
	// Quick is where a task typed above the rows goes; nil when no list takes tasks.
	Quick *quickList
}

// TaskRow is one row of the task list.
type TaskRow struct {
	Task     calendar.TaskObject
	Calendar dav.Collection
	Summary  string
	// AddedBy names who added a task, where not the row's own account.
	AddedBy string
	Status  string
	Due     time.Time
	// Added is CREATED; zero where the writer sent none, since DTSTAMP moves with every change.
	Added     time.Time
	Completed bool
	// Priority is the band the task's PRIORITY falls in, or empty.
	Priority string
	// Star is the colour the task is marked in, or empty.
	Star string
	// Href is the task's page with the list carried along.
	Href string
}

var taskListParams = []string{"account", "cal", "query", "view", "sort", "dir", "page", "ipp"}

// Open tasks are the list itself, not a view.
const (
	viewCompleted = "completed"
	viewAll       = "all"
	viewHigh      = "high"
)

type quickList struct {
	Account string
	Path    string
	Name    string
	// Lists are the task lists the line can write into, by account.
	Lists []dav.Group
	// Only says the page shows one list, so no picker is needed.
	Only bool
}

func quickListOf(ctx *web.Context, calendars []dav.Collection) *quickList {
	scope := ctx.URLAccount()
	var quick *quickList
	var lists []dav.Group
	indexes := make(map[string]int)
	shown := 0
	for _, cal := range calendars {
		if !cal.Writable || !calendar.SupportsTodo(cal.Components) || (scope != "" && cal.Account != scope) {
			continue
		}
		// The list in force, else the first shown.
		if cal.Shown {
			shown++
			if quick == nil {
				quick = &quickList{Account: cal.Account, Path: cal.Path, Name: cal.Name}
			}
		}
		at, ok := indexes[cal.Account]
		if !ok {
			lists = append(lists, dav.Group{Account: cal.Account})
			at = len(lists) - 1
			indexes[cal.Account] = at
		}
		lists[at].Collections = append(lists[at].Collections, cal)
	}
	if quick == nil {
		return nil
	}
	quick.Lists = lists
	quick.Only = shown == 1
	return quick
}

func taskRows(ctx *web.Context, view string) []web.FilterRow {
	reset := ctx.WithoutParam("view")
	row := func(label, name string) web.FilterRow {
		href := ctx.WithParam("view", name)
		if name == view {
			href = reset
		}
		return web.FilterRow{Label: label, Href: href, Current: name == view}
	}
	rows := []web.FilterRow{
		row(ctx.T("tasks.completed"), viewCompleted),
		row(ctx.T("tasks.all"), viewAll),
		row(ctx.T("tasks.priorityhigh"), viewHigh),
	}
	return append(rows, web.ViewRows(ctx, view, false, reset)...)
}

type TaskList = CalendarList[TaskRow]

func (p *plugin) taskList(ctx *web.Context) (TaskList, error) {
	return calendarList(ctx, "calendar.tasks", taskListParams,
		func(view string) bool {
			return view == viewCompleted || view == viewAll || view == viewHigh || web.ValidStarView(view)
		},
		func(view string, q search.Query, params url.Values) ([]TaskRow, []dav.Collection, []pim.Unread, error) {
			return p.shownTasks(ctx, pimweb.Only(ctx, "cal"), view, q, params)
		}, taskColumns, taskTie, func(row TaskRow) pimweb.Item { return pimweb.Item{Path: row.Task.Path, URL: row.Href} })
}

// shownTasks are the rows of the tasks on the list a view and a query make, from the lists in the
// request's scope, and every list for the rail, with the lists and tasks it could not read;
// params go into each row's link.
func (p *plugin) shownTasks(ctx *web.Context, only map[string]bool, view string, q search.Query, params url.Values) ([]TaskRow, []dav.Collection, []pim.Unread, error) {
	star := view
	if view == viewCompleted || view == viewAll || view == viewHigh {
		star = ""
	}
	return shownCalendarRows(p, ctx, only, q, star, calendar.SupportsTodo, calendar.TaskVisibility, calendar.TaskQuery,
		calendar.FirstTodo, func(todo *ical.Component, starred func(string) bool, q search.Query) bool {
			return taskShown(todo, view, starred, q)
		}, func(obj *caldav.CalendarObject, collection dav.Collection) TaskRow {
			return taskRow(ctx, obj, collection, web.UserLocation(ctx), params)
		})
}

func taskTie(row TaskRow) string {
	return strings.ToLower(row.Task.Account + "\x00" + row.Calendar.Name + "\x00" + row.Summary)
}

// taskShown says whether a to-do is on the list a view, its stars and a query make: open ones unless
// the view says otherwise, the stars kept, the words in its summary or description.
func taskShown(todo *ical.Component, view string, starred func(string) bool, q search.Query) bool {
	if todo == nil {
		return false
	}
	status, _ := todo.Props.Text("STATUS")
	// The page hides completed tasks even when a server ignores the STATUS filter.
	if (status == "COMPLETED") != (view == viewCompleted) && view != viewAll {
		return false
	}
	if !starred(calendar.ComponentColor(todo)) {
		return false
	}
	if view == viewHigh && calendar.PriorityBand(todo) != calendar.PriorityHigh {
		return false
	}
	summary, _ := todo.Props.Text("SUMMARY")
	description, _ := todo.Props.Text("DESCRIPTION")
	return pim.Matches(q, summary, description)
}

var taskColumns = []pim.Column[TaskRow]{
	{Key: "summary", Value: func(row TaskRow) string { return strings.ToLower(row.Summary) }},
	{Key: "status", Value: func(row TaskRow) string {
		if row.Completed {
			return "1"
		}
		return "0"
	}},
	// High first; a task with none after the low ones.
	{Key: "priority", Value: func(row TaskRow) string {
		if i := slices.Index(calendar.PriorityBands, row.Priority); i >= 0 {
			return strconv.Itoa(i)
		}
		return strconv.Itoa(len(calendar.PriorityBands))
	}},
	{Key: alborz.ViewStarred, Value: func(row TaskRow) string { return pim.StarredFirst(row.Star) }},
	{Key: "account", Value: func(row TaskRow) string { return strings.ToLower(row.Task.Account) }},
	{Key: "calendar", Value: func(row TaskRow) string { return strings.ToLower(row.Calendar.Name) }},
	{Key: "due", Value: func(row TaskRow) string { return pim.When(row.Due) }},
	{Key: "added", Value: func(row TaskRow) string { return pim.When(row.Added) }},
}

func (p *plugin) tasks(ctx *web.Context) error {
	list, err := p.taskList(ctx)
	if err != nil {
		return err
	}
	rows, pager, invitations, err := calendarListParts(ctx, p, list, func(row TaskRow) (string, string) { return row.Calendar.Account, row.Task.Path }, func(row *TaskRow, value string) { row.AddedBy = value })
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "tasks/list", &TasksRenderData{
		ListRenderData: calendarListData(ctx, ctx.T("title.tasks"), list, pager, calendarFilters(ctx), taskRows(ctx, list.View), tasksForm, invitations),
		Quick:          quickListOf(ctx, list.Calendars), Tasks: rows,
	})
}

func (p *plugin) complete(ctx *web.Context) error {
	done := wantsDone(ctx)
	words := func(ctx *web.Context, marked []pim.Ref[*caldav.Client], next string) web.Notice {
		paths := make([]string, len(marked))
		for i, ref := range marked {
			paths[i] = ref.Account + "|" + ref.Path
		}
		return completedNotice(ctx, done, len(marked), ctx.Path("calendar.tasks.complete"), url.Values{"paths": paths, "next": {next}})
	}
	var marked *caldav.CalendarObject
	return pimweb.Run(ctx, pimweb.Action[*caldav.Client]{
		Client: p.Client,
		List:   "calendar.tasks",
		Do: func(ctx *web.Context, ref pim.Ref[*caldav.Client]) (err error) {
			marked, err = calendar.ChangeComponent(ctx.Request().Context(), ref, calendar.FirstTodo, func(todo *ical.Component) { calendar.MarkTodo(todo, done) })
			return err
		},
		Done: words,
		Piece: func(ctx *web.Context, ref pim.Ref[*caldav.Client], next string) error {
			// A task the list no longer shows leaves it, with a notice that brings it back.
			view := web.KeepURLParams(next, "view").Get("view")
			if view != viewAll && done != (view == viewCompleted) {
				ctx.Notify(words(ctx, []pim.Ref[*caldav.Client]{ref}, next))
				return ctx.Redraw(next)
			}
			// The same button undoes it, so a marked task answers with its row and no notice.
			calendars, err := p.DAV.Collections(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return err
			}
			holder := dav.Holding(calendars, "", ref.Path)
			if holder == nil {
				return calendar.ErrNoCalendar
			}
			// Only the pooled listing names the calendar's account, and the row's links need it.
			cal := *holder
			cal.Account = ref.Account
			// The list's shape is in the return address, not the write's own URL.
			params := web.KeepURLParams(next, taskListParams...)
			params.Set("from", next)
			row := taskRow(ctx, marked, cal, web.UserLocation(ctx), params)
			row.AddedBy = p.DAV.AddedBy(ctx.Login.Username())(cal.Account, marked.Path)
			data := &TaskRowRenderData{BaseRenderData: *web.NewBaseRenderData(ctx), Row: row, Next: next}
			if ctx.FormValue("select") == tasksForm {
				data.Select = tasksForm
			}
			data.G = &data.BaseRenderData
			return ctx.Render(http.StatusOK, "task-row", data)
		},
	})
}

// TaskRowRenderData is one task's row, re-rendered when it is marked.
type TaskRowRenderData struct {
	web.BaseRenderData
	// G hands the row template the page's globals, the page being absent.
	G    *web.BaseRenderData
	Row  TaskRow
	Next string
	// Select is the list's selection form, as the row was drawn; empty where it offered none.
	Select string
}

// tasksForm is the task list's selection form.
const tasksForm = "tasks-form"

func taskRow(ctx *web.Context, task *caldav.CalendarObject, cal dav.Collection, loc *time.Location, params url.Values) TaskRow {
	todo := calendar.FirstTodo(task.Data)
	summary, _ := todo.Props.Text("SUMMARY")
	status, _ := todo.Props.Text("STATUS")
	// A writer with only DTSTART is still saying when the task is for.
	due, _ := todo.Props.DateTime(ical.PropDue, loc)
	if due.IsZero() {
		due, _ = todo.Props.DateTime(ical.PropDateTimeStart, loc)
	}
	added, _ := todo.Props.DateTime(ical.PropCreated, loc)
	object := calendar.TaskObject{CalendarObject: task, Account: cal.Account}
	return TaskRow{
		Task:      object,
		Href:      pimweb.ObjectURL(ctx, "calendar.task", task.Path, object.Account, params),
		Calendar:  cal,
		Summary:   summary,
		Status:    status,
		Due:       due,
		Added:     added,
		Completed: status == "COMPLETED",
		Priority:  calendar.PriorityBand(todo),
		Star:      calendar.ComponentColor(todo),
	}
}

func wantsDone(ctx *web.Context) bool {
	return (ctx.FormValue("reopen") == "") != (ctx.FormValue("undo") != "")
}

func completedNotice(ctx *web.Context, done bool, n int, action string, form url.Values) web.Notice {
	key := "notice.tasksdone"
	if !done {
		key = "notice.tasksopen"
		form.Set("reopen", "1")
	}
	return web.Notice{Kind: web.NoticeDone, Text: ctx.Tf(key, n), Actions: []*web.NoticeAction{ctx.Undo(action, form)}}
}

// Copy and delete, since the lists may live on different servers.
func (p *plugin) move(ctx *web.Context) error {
	params, err := ctx.FormParams()
	if err != nil {
		return err
	}
	page := func(status int, refusal string) error {
		groups, err := p.writableGroups(ctx, calendar.SupportsTodo)
		if err != nil {
			return err
		}
		rail, err := p.taskRail(ctx)
		if err != nil {
			return err
		}
		data := destinationRenderData(ctx, rail, ctx.T("nav.tasks"), ctx.Path("calendar.tasks"), ctx.T("folder.moveask"),
			ctx.Tf("tasks.movecount", len(params["paths"])), ctx.T("tasks.movelist"), ctx.T("folder.movedo"), "move", groups,
			ctx.NextOr(ctx.URL("calendar.tasks")))
		data.Action, data.Paths = ctx.Path("calendar.tasks.move"), params["paths"]
		data.Refused(refusal)
		return ctx.Render(status, "calendar/destination", data)
	}
	if ctx.Request().Method != http.MethodPost {
		return page(http.StatusOK, "")
	}
	target, err := p.destination(ctx, params.Get("to"), calendar.SupportsTodo)
	if handled, refusal := pimweb.DestinationRefusal(ctx, err, func(message string) error {
		return page(http.StatusUnprocessableEntity, message)
	}); handled {
		return refusal
	}
	if err != nil {
		return err
	}
	return pimweb.Run(ctx, pimweb.Action[*caldav.Client]{Client: p.Client, List: "calendar.tasks",
		Done: func(ctx *web.Context, done []pim.Ref[*caldav.Client], _ string) web.Notice {
			return web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.tasksmoved", len(done))}
		},
		Do: func(ctx *web.Context, ref pim.Ref[*caldav.Client]) error {
			if path.Dir(ref.Path)+"/" == target.Path {
				return nil
			}
			co, err := calendar.GetObject(ctx.Request().Context(), ref.Client, ref.Path)
			if err != nil {
				return fmt.Errorf("failed to get task: %w", err)
			}
			made, err := target.Client.PutCalendarObject(ctx.Request().Context(), target.Path+path.Base(ref.Path), co.Data,
				&caldav.PutCalendarObjectOptions{IfNoneMatch: dav.IfNew})
			if err != nil {
				return err
			}
			// A list that keeps its task, a read-only share, leaves it where it was rather than in two lists.
			if err := pimweb.Delete(ctx, ref); err != nil {
				return errors.Join(err, target.Client.RemoveAll(ctx.Request().Context(), made.Path))
			}
			return nil
		}})
}
