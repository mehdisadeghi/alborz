package web

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
)

type TaskLink struct {
	UID, Path, Summary, Href string
	Completed                bool
}

func taskLink(ctx *web.Context, path string, todo *ical.Component) TaskLink {
	uid, _ := todo.Props.Text(ical.PropUID)
	summary, _ := todo.Props.Text(ical.PropSummary)
	status, _ := todo.Props.Text(ical.PropStatus)
	return TaskLink{UID: uid, Path: path, Summary: summary, Completed: status == "COMPLETED",
		Href: pimweb.ObjectURL(ctx, "calendar.task", path, ctx.Login.Username(), nil)}
}

// taskRelations reads the same collection regardless of list filters: completed
// children and a parent outside the current search remain part of the task.
func taskRelations(ctx *web.Context, page *calendarObjectPage) (*TaskLink, []TaskLink, error) {
	query := calendar.TaskQuery()
	objects, err := page.client.QueryCalendar(ctx.Request().Context(), page.collection.Path, &query)
	if err != nil {
		return nil, nil, err
	}
	uid, _ := page.component.Props.Text(ical.PropUID)
	parentUID := calendar.ParentUID(page.component)
	var parent *TaskLink
	var children []TaskLink
	for _, object := range objects {
		if object.Path == page.object.Path || object.Data == nil {
			continue
		}
		todo := calendar.FirstTodo(object.Data)
		if todo == nil {
			continue
		}
		link := taskLink(ctx, object.Path, todo)
		if parentUID != "" && link.UID == parentUID {
			parent = &link
		}
		if uid != "" && calendar.ParentUID(todo) == uid {
			children = append(children, link)
		}
	}
	sort.SliceStable(children, func(i, j int) bool {
		if children[i].Completed != children[j].Completed {
			return !children[i].Completed
		}
		return strings.ToLower(children[i].Summary) < strings.ToLower(children[j].Summary)
	})
	return parent, children, nil
}

// newSubtask binds a new child to a verified parent in the same account/list.
// Existing tasks retain their relationships unchanged when edited.
func (p *plugin) newSubtask(ctx *web.Context, edit *calendarEdit) (*TaskLink, error) {
	path := ctx.FormValue("parent")
	if !edit.creating() || path == "" {
		return nil, nil
	}
	parent, err := p.openCalendarObject(ctx, path, "notfound.task", "failed to get parent task", fmt.Errorf("no VTODO component found"), calendar.FirstTodo)
	if err != nil {
		return nil, err
	}
	if !parent.collection.Writable {
		return nil, web.NewError(http.StatusForbidden, ctx.T("form.readonlysource"))
	}
	link := taskLink(ctx, path, parent.component)
	if link.UID == "" {
		return nil, fmt.Errorf("%w: parent task has no UID", alborz.ErrUnreadable)
	}
	// Only offer the parent's list if it remains a visible destination.
	var groups []dav.Group
	for _, group := range edit.groups {
		if group.Account != ctx.Login.Username() {
			continue
		}
		if collection := dav.At(group.Collections, parent.collection.Path); collection != nil {
			groups = []dav.Group{{Account: group.Account, Collections: []dav.Collection{*collection}}}
			edit.collection = collection
		}
	}
	edit.groups = groups
	calendar.SetParent(edit.component, link.UID)
	return &link, nil
}

func childCreateURL(ctx *web.Context, page *calendarObjectPage) string {
	return web.QueryPath(ctx.Path("calendar.task.create"), url.Values{
		"account": {ctx.Login.Username()}, "parent": {page.object.Path}, "next": {ctx.Request().URL.RequestURI()},
	})
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
