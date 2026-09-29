package calendar

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
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/labstack/echo/v4"
)

// MoveTasksRenderData is the chooser a move without a destination answers with.
type MoveTasksRenderData struct {
	alborz.BaseRenderData
	Rail      pim.Rail
	Calendars []dav.Collection
	Paths     []string
	Next      string
}

type TasksRenderData struct {
	alborz.BaseRenderData
	Calendars []dav.Collection
	Tasks     []TaskRow
	// Quick is where a task typed above the rows goes; nil when no list takes tasks.
	Quick      *quickList
	View       string
	Filters    []alborz.Filter
	FilterRows []alborz.FilterRow
	Query      string
	Sorting    pim.Sorting
	Pager      alborz.Pager

	Invitations *dav.RailInvitations
}

// TaskRow is one row of the task list.
type TaskRow struct {
	Task     TaskObject
	Calendar dav.Collection
	Summary  string
	// AddedBy names who added a task, where not the row's own account.
	AddedBy string
	Status  string
	Due     time.Time
	// Added is DTSTAMP, else CREATED; zero where the writer sent neither.
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

func quickListOf(ctx *alborz.Context, calendars []dav.Collection) *quickList {
	scope := ctx.URLAccount()
	var quick *quickList
	var lists []dav.Group
	shown := 0
	for _, cal := range calendars {
		if !cal.Writable || !supportsTodo(cal.Components) || (scope != "" && cal.Account != scope) {
			continue
		}
		// The list in force, else the first shown.
		if cal.Shown {
			shown++
			if quick == nil {
				quick = &quickList{Account: cal.Account, Path: cal.Path, Name: cal.Name}
			}
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
	quick.Only = shown == 1
	return quick
}

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
	return append(rows, alborz.ViewRows(ctx, view, false, clear)...)
}

// TaskList is the list page's rows in order, their lists, and what shaped them.
type TaskList struct {
	Rows      []TaskRow
	Calendars []dav.Collection
	// Items are the rows as a single task's neighbours, in the same order.
	Items   []pim.Item
	Query   string
	View    string
	Sorting pim.Sorting
}

func (p *plugin) taskList(ctx *alborz.Context) (TaskList, error) {
	var rows []TaskRow
	loc := alborz.UserLocation(ctx)
	only := pim.Only(ctx, "cal")
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return TaskList{}, err
	}

	calendarInfos, sites, err := visibleCalendars(ctx.Request().Context(), accounts, ctx.URLAccount(), only, supportsTodo, taskVisibility)
	if err != nil {
		return TaskList{}, err
	}
	search := ctx.QueryParam("query")
	// A star is a view, as in the mail rail; open tasks are the list with no view.
	view := ctx.QueryParam("view")
	if view != viewCompleted && view != viewAll && view != viewHigh && !validStarView(view) {
		return TaskList{}, echo.NewHTTPError(http.StatusBadRequest, "no such view")
	}
	star := view
	withCompleted := view == viewCompleted || view == viewAll
	if withCompleted || view == viewHigh {
		star = ""
	}
	params := pim.RowParams(ctx, "/tasks", pim.ListParams(ctx, taskListParams...))

	query := taskQuery()

	for _, result := range pim.Present(p.dav, dav.Each(ctx.Request().Context(), sites, func(ctx context.Context, site pim.Site[*caldav.Client]) ([]caldav.CalendarObject, error) {
		return site.Client.QueryCalendar(ctx, site.Collection.Path, &query)
	})) {
		if result.Err != nil {
			return TaskList{}, fmt.Errorf("failed to query tasks from %s: %v", result.Site.Collection.Name, result.Err)
		}

		for _, task := range result.Value {
			todo := getFirstTodo(task.Data)
			if todo == nil {
				continue
			}
			status, _ := todo.Props.Text("STATUS")
			// The page hides completed tasks even when a server ignores the STATUS filter.
			completed := status == "COMPLETED"
			if completed != (view == viewCompleted) && view != viewAll {
				continue
			}
			if !alborz.StarMatches(componentColor(todo), star) {
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

	sorting, err := pim.Sort(ctx, rows, taskColumns, func(row TaskRow) string {
		return strings.ToLower(row.Task.Account + "\x00" + row.Calendar.Name + "\x00" + row.Summary)
	}, taskListParams...)
	if err != nil {
		return TaskList{}, err
	}
	items := make([]pim.Item, len(rows))
	for i, row := range rows {
		items[i] = pim.Item{Path: row.Task.Path, URL: row.Href}
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
		if i := slices.Index(priorityBands, row.Priority); i >= 0 {
			return strconv.Itoa(i)
		}
		return strconv.Itoa(len(priorityBands))
	}},
	{Key: alborz.ViewStarred, Value: func(row TaskRow) string { return pim.StarredFirst(row.Star) }},
	{Key: "account", Value: func(row TaskRow) string { return strings.ToLower(row.Task.Account) }},
	{Key: "calendar", Value: func(row TaskRow) string { return strings.ToLower(row.Calendar.Name) }},
	{Key: "due", Value: func(row TaskRow) string { return pim.When(row.Due) }},
	{Key: "added", Value: func(row TaskRow) string { return pim.When(row.Added) }},
}

func (p *plugin) tasks(ctx *alborz.Context) error {
	list, err := p.taskList(ctx)
	if err != nil {
		return err
	}
	rows, pager, err := alborz.Paginate(ctx, list.Rows)
	if err != nil {
		return err
	}
	addedBy := p.dav.AddedBy(ctx.Login.Username())
	for i := range rows {
		rows[i].AddedBy = addedBy(rows[i].Calendar.Account, rows[i].Task.Path)
	}
	invitations, err := p.invitations(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "tasks/list", &TasksRenderData{
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
		Invitations:    invitations,
	})
}

func (p *plugin) complete(ctx *alborz.Context) error {
	done := wantsDone(ctx)
	words := func(ctx *alborz.Context, marked []pim.Ref[*caldav.Client], next string) alborz.Notice {
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
	return pim.Run(ctx, pim.Action[*caldav.Client]{
		Client: p.client,
		List:   "/tasks",
		Do: func(ctx *alborz.Context, ref pim.Ref[*caldav.Client]) (err error) {
			marked, err = changeComponent(ctx, ref, getFirstTodo, func(todo *ical.Component) { markTodo(todo, done) })
			return err
		},
		Done: words,
		Piece: func(ctx *alborz.Context, ref pim.Ref[*caldav.Client], next string) error {
			// A task the list no longer shows leaves it, with a notice that brings it back.
			view := pim.ListParamsIn(next, "view").Get("view")
			if view != viewAll && done != (view == viewCompleted) {
				ctx.Notify(words(ctx, []pim.Ref[*caldav.Client]{ref}, next))
				return ctx.Relocate(next)
			}
			// The same button undoes it, so a marked task answers with its row and no notice.
			calendars, err := p.dav.Collections(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return err
			}
			holder := dav.Holding(calendars, "", ref.Path)
			if holder == nil {
				return errNoCalendar
			}
			// Only the pooled listing names the calendar's account, and the row's links need it.
			cal := *holder
			cal.Account = ref.Account
			// The list's shape is in the return address, not the write's own URL.
			params := pim.ListParamsIn(next, taskListParams...)
			params.Set("from", next)
			row := taskRow(marked, cal, alborz.UserLocation(ctx), params)
			row.AddedBy = p.dav.AddedBy(ctx.Login.Username())(cal.Account, marked.Path)
			data := &TaskRowRenderData{BaseRenderData: *alborz.NewBaseRenderData(ctx), Row: row, Next: next}
			data.G = &data.BaseRenderData
			return ctx.Render(http.StatusOK, "task-row", data)
		},
	})
}

// TaskRowRenderData is one task's row, re-rendered when it is marked.
type TaskRowRenderData struct {
	alborz.BaseRenderData
	// G hands the row template the page's globals, the page being absent.
	G    *alborz.BaseRenderData
	Row  TaskRow
	Next string
}

func taskRow(task *caldav.CalendarObject, cal dav.Collection, loc *time.Location, params url.Values) TaskRow {
	todo := getFirstTodo(task.Data)
	summary, _ := todo.Props.Text("SUMMARY")
	status, _ := todo.Props.Text("STATUS")
	// A writer with only DTSTART is still saying when the task is for.
	due, _ := todo.Props.DateTime(ical.PropDue, loc)
	if due.IsZero() {
		due, _ = todo.Props.DateTime(ical.PropDateTimeStart, loc)
	}
	added, _ := todo.Props.DateTime(ical.PropDateTimeStamp, loc)
	if added.IsZero() {
		added, _ = todo.Props.DateTime(ical.PropCreated, loc)
	}
	object := TaskObject{CalendarObject: task, Account: cal.Account}
	return TaskRow{
		Task:      object,
		Href:      pim.ObjectURL("/tasks/", task.Path, object.Account, params),
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

func wantsDone(ctx *alborz.Context) bool {
	return (ctx.FormValue("reopen") == "") != (ctx.FormValue("undo") != "")
}

func completedNotice(ctx *alborz.Context, done bool, n int, action string, form url.Values) alborz.Notice {
	key := "notice.tasksdone"
	if !done {
		key = "notice.tasksopen"
		form.Set("reopen", "1")
	}
	return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.Tf(key, n), Actions: []*alborz.NoticeAction{ctx.Undo(action, form)}}
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

// Copy and delete, since the lists may live on different servers.
func (p *plugin) move(ctx *alborz.Context) error {
	params, err := ctx.FormParams()
	if err != nil {
		return err
	}
	// A menu holds actions; the destination is chosen on a page this route answers with.
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
		return ctx.Render(http.StatusOK, "tasks/move", &MoveTasksRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("folder.moveask")),
			Rail:           rail,
			Calendars:      list.Calendars,
			Paths:          params["paths"],
			Next:           ctx.NextOr(ctx.AccountPath("/tasks")),
		})
	}
	targets, err := pim.Selected(ctx, []string{to}, p.client)
	if err != nil {
		return err
	}
	target := targets[0]
	return pim.Run(ctx, pim.Action[*caldav.Client]{Client: p.client, List: "/tasks",
		Done: func(ctx *alborz.Context, done []pim.Ref[*caldav.Client], _ string) alborz.Notice {
			return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.Tf("notice.tasksmoved", len(done))}
		},
		Do: func(ctx *alborz.Context, ref pim.Ref[*caldav.Client]) error {
			if path.Dir(ref.Path)+"/" == target.Path {
				return nil
			}
			co, err := ref.Client.GetCalendarObject(ctx.Request().Context(), ref.Path)
			if err != nil {
				return fmt.Errorf("failed to get task: %v", err)
			}
			if _, err := target.Client.PutCalendarObject(ctx.Request().Context(), target.Path+path.Base(ref.Path), co.Data,
				&caldav.PutCalendarObjectOptions{IfNoneMatch: dav.IfNew}); err != nil {
				return err
			}
			return pim.Delete(ctx, ref)
		}})
}

func taskQuery() caldav.CalendarQuery {
	return caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:  "VCALENDAR",
			Props: []string{"VERSION"},
			// The whole to-do: every column reads a property of it.
			Comps: []caldav.CalendarCompRequest{{Name: "VTODO", AllProps: true, AllComps: true}},
		},
		CompFilter: caldav.CompFilter{
			Name:  "VCALENDAR",
			Comps: []caldav.CompFilter{{Name: "VTODO"}},
		},
	}
}
