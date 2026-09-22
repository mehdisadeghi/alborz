package alborzcaldav

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/labstack/echo/v4"
)

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
	Pager      dav.Pager
}

// TaskRow is the flat, table-shaped representation shared by the task
// list and its sort controls. Calendar ownership stays explicit instead
// of being encoded as nested visual groups.
type TaskRow struct {
	Task     TaskObject
	Calendar dav.Collection
	Summary  string
	// AddedBy names who added a task kept here, where not the row's own
	// account; set for the rows drawn, not every row of the list.
	AddedBy string
	Status  string
	Due     time.Time
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

// The task list's own views, beside the stars: open tasks are the
// list itself and no view.
const (
	viewCompleted = "completed"
	viewAll       = "all"
	viewHigh      = "high"
)

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
	params := dav.RowParams(ctx, "/tasks", dav.ListParams(ctx, taskListParams...))

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
			if !alborzbase.StarMatches(componentColor(todo), star) {
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
	rows, pager := dav.Paginate(ctx, list.Rows)
	addedBy := p.dav.AddedBy(ctx.Session.Username())
	for i := range rows {
		rows[i].AddedBy = addedBy(rows[i].Calendar.Account, rows[i].Task.Path)
	}
	return ctx.Render(http.StatusOK, "tasks.html", &TasksRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("title.tasks")),
		Quick:          quickListOf(ctx, list.Calendars),
		View:           list.View,
		Filters:        calendarFilters(ctx, list.Calendars),
		FilterRows:     taskRows(ctx, list.View),
		Calendars:      list.Calendars,
		Tasks:          rows,
		Pager:          pager,
		Query:          list.Query,
		Sorting:        list.Sorting,
	})
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
			// A task the list no longer shows - done where the done are
			// hidden, reopened in the list of the done - leaves it, as a
			// moved message leaves its folder, with the notice that brings
			// it back.
			view := dav.ListParamsIn(next, "view").Get("view")
			if view != viewAll && done != (view == viewCompleted) {
				ctx.Notify(words(ctx, []dav.Ref[*caldav.Client]{ref}, next))
				return ctx.Relocate(next)
			}
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
			params := dav.ListParamsIn(next, taskListParams...)
			params.Set("from", next)
			row := taskRow(marked, cal, alborzbase.UserLocation(ctx), params)
			row.AddedBy = p.dav.AddedBy(ctx.Session.Username())(cal.Account, marked.Path)
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
