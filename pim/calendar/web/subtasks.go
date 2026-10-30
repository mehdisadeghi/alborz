package web

import (
	"slices"
	"sort"
	"strings"

	"git.mehdix.org/alborz/pim/calendar"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

type TaskLink struct {
	UID, Path, Summary, Href string
	Completed                bool
	parent                   string
}

func taskLink(ctx *web.Context, path string, todo *ical.Component) TaskLink {
	uid, _ := todo.Props.Text(ical.PropUID)
	summary, _ := todo.Props.Text(ical.PropSummary)
	status, _ := todo.Props.Text(ical.PropStatus)
	return TaskLink{UID: uid, Path: path, Summary: summary, Completed: status == "COMPLETED", parent: calendar.ParentUID(todo),
		Href: pimweb.ObjectURL(ctx, "calendar.task", path, ctx.Login.Username(), nil)}
}

// collectionTasks reads the whole collection regardless of list filters: completed
// children and a parent outside the current search remain part of the task.
func collectionTasks(ctx *web.Context, client *caldav.Client, collection string) ([]TaskLink, error) {
	query := calendar.TaskQuery()
	objects, err := client.QueryCalendar(ctx.Request().Context(), collection, &query)
	if err != nil {
		return nil, err
	}
	var tasks []TaskLink
	for _, object := range objects {
		if object.Data == nil {
			continue
		}
		if todo := calendar.FirstTodo(object.Data); todo != nil {
			tasks = append(tasks, taskLink(ctx, object.Path, todo))
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Completed != tasks[j].Completed {
			return !tasks[i].Completed
		}
		return strings.ToLower(tasks[i].Summary) < strings.ToLower(tasks[j].Summary)
	})
	return tasks, nil
}

func taskRelations(ctx *web.Context, page *calendarObjectPage) (*TaskLink, []TaskLink, error) {
	tasks, err := collectionTasks(ctx, page.client, page.collection.Path)
	if err != nil {
		return nil, nil, err
	}
	uid, _ := page.component.Props.Text(ical.PropUID)
	parentUID := calendar.ParentUID(page.component)
	var parent *TaskLink
	var children []TaskLink
	for _, task := range tasks {
		if task.Path == page.object.Path {
			continue
		}
		if parentUID != "" && task.UID == parentUID {
			parent = &task
		}
		if uid != "" && task.parent == uid {
			children = append(children, task)
		}
	}
	return parent, children, nil
}

// parentChoices offers every task but uid and its descendants, which would close a cycle.
func parentChoices(tasks []TaskLink, uid string) []TaskLink {
	excluded := map[string]bool{"": true, uid: true}
	for grew := true; grew; {
		grew = false
		for _, task := range tasks {
			if !excluded[task.UID] && task.parent != "" && excluded[task.parent] {
				excluded[task.UID], grew = true, true
			}
		}
	}
	return slices.DeleteFunc(tasks, func(task TaskLink) bool { return excluded[task.UID] })
}

// taskParents annotates relationships without changing the reader's sort order.
func taskParents(rows []TaskRow) {
	parents := make(map[string]TaskLink, len(rows))
	key := func(row TaskRow, uid string) string {
		return row.Task.Account + "\x00" + row.Calendar.Path + "\x00" + uid
	}
	for _, row := range rows {
		todo := calendar.FirstTodo(row.Task.Data)
		uid, _ := todo.Props.Text(ical.PropUID)
		if uid != "" {
			parents[key(row, uid)] = TaskLink{UID: uid, Path: row.Task.Path, Summary: row.Summary, Href: row.Href, Completed: row.Completed}
		}
	}
	for i := range rows {
		if uid := calendar.ParentUID(calendar.FirstTodo(rows[i].Task.Data)); uid != "" {
			if parent, ok := parents[key(rows[i], uid)]; ok && parent.Path != rows[i].Task.Path {
				rows[i].Parent = &parent
			}
		}
	}
}
