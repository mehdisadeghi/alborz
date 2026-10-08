package web

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"time"

	"git.mehdix.org/alborz/internal/dates"
	"git.mehdix.org/alborz/pim/calendar"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
)

type TaskRenderData struct {
	CalendarObjectRenderData
	Task calendar.TaskObject
	// Authors are who added and last changed the object, where another account.
	// List is the list the page was opened from.
	Star     string
	Priority string
}

type UpdateTaskRenderData struct {
	UpdateCalendarObjectRenderData
	Todo *ical.Component
	// Due is the due date as the field holds it.
	Due dates.Field
	// Priority is the band the select holds; PriorityBands the bands offered.
	Priority      string
	PriorityBands []string
}

func (p *plugin) task(ctx *web.Context) error {
	path := ctx.Param("path")
	e, err := p.openCalendarObject(ctx, path, "notfound.task", "failed to get task", fmt.Errorf("no VTODO component found"), calendar.FirstTodo)
	if err != nil {
		return err
	}
	task, todo := e.object, e.component
	summary, _ := todo.Props.Text("SUMMARY")

	rail, err := p.taskRail(ctx)
	if err != nil {
		return err
	}
	list, err := p.taskList(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "tasks/task", &TaskRenderData{
		CalendarObjectRenderData: pimweb.NewObjectRenderData(ctx, summary, rail, e.collection,
			p.DAV.Authors(task.Path, ctx.Login.Username()), cmp.Or(ctx.From(), web.QueryPath(ctx.Path("calendar.tasks"), web.ReadListState(ctx).Keep(taskListParams...))), calendar.ComponentColor(todo), pimweb.Around(list.Items, path)),
		Task:     calendar.TaskObject{CalendarObject: task},
		Star:     calendar.ComponentColor(todo),
		Priority: calendar.PriorityBand(todo),
	})
}
func (p *plugin) updateTask(ctx *web.Context) error {
	taskPath := ctx.Param("path")
	loc := web.UserLocation(ctx)
	e, err := p.editCalendarObject(ctx, taskPath, "notfound.task", "failed to get task", fmt.Errorf("no VTODO component found"), calendar.SupportsTodo,
		calendar.FirstTodo, func() *ical.Component {
			todo := ical.NewComponent(ical.CompToDo)
			todo.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
			return todo
		})
	if err != nil {
		return err
	}
	todo := e.component

	if ctx.Request().Method == "POST" {
		summary := ctx.FormValue("summary")
		description := ctx.FormValue("description")
		dueDate := ctx.DateField("due-date")
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		// A form without the select changes no priority.
		_, prioritySent := params["priority"]
		band := ctx.FormValue("priority")
		if band != "" && !slices.Contains(calendar.PriorityBands, band) {
			return web.NewError(http.StatusBadRequest, "no such priority")
		}

		reject := func(message string) error {
			rail, err := p.taskRail(ctx)
			if err != nil {
				return err
			}
			return ctx.Render(http.StatusUnprocessableEntity, "tasks/task-edit", &UpdateTaskRenderData{
				PageData:      *web.NewPageData(ctx, ctx.T("tasks.createtitle"), rail).WithRefused(message),
				Groups:        e.groups,
				Collection:    e.collection,
				Object:        e.object,
				Todo:          todo,
				Due:           dueDate,
				Priority:      band,
				PriorityBands: calendar.PriorityBands,
			})
		}
		if summary == "" {
			return reject(ctx.T("form.summaryneeded"))
		}

		to, err := e.destination(ctx, p, calendar.SupportsTodo)
		if handled, refusal := pimweb.DestinationRefusal(ctx, err, reject); handled {
			return refusal
		}
		if err != nil {
			return err
		}
		creating := e.creating()

		todo.Props.SetText(ical.PropSummary, summary)

		setDescription(todo, description, true)

		// The zone definition must cover the date it qualifies, so it brackets the due date.
		due := time.Now().In(loc)
		if dueDate.Value != "" {
			at, err := dueDate.Read(loc)
			if err != nil {
				return reject(ctx.T("form.duedate"))
			}
			todo.Props.SetDateTime(ical.PropDue, at)
			due = at
		} else {
			todo.Props.Del(ical.PropDue)
		}
		// alborz keeps the date in DUE, where the form shows it.
		todo.Props.Del(ical.PropDateTimeStart)
		if prioritySent {
			calendar.SetPriorityBand(todo, band)
		}

		if todo.Props.Get(ical.PropUID) == nil {
			todo.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
		}
		_, err = e.put(ctx, to, todo, due)
		if err != nil {
			return reject(fmt.Sprintf(ctx.T("form.saverefused"), err))
		}

		return pimweb.Saved(ctx, creating, ctx.T("notice.taskcreated"), summary, ctx.Path("calendar.task", e.object.Path), ctx.Path("calendar.tasks"), to.Account)
	}

	summary, _ := todo.Props.Text("SUMMARY")
	// DUE, or DTSTART from a writer that sent only the start.
	at, err := todo.Props.DateTime(ical.PropDue, loc)
	if err != nil {
		return err
	}
	if at.IsZero() {
		if at, err = todo.Props.DateTime(ical.PropDateTimeStart, loc); err != nil {
			return err
		}
	}

	rail, err := p.taskRail(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "tasks/task-edit", &UpdateTaskRenderData{
		PageData:      *web.NewPageData(ctx, fmt.Sprintf(ctx.T("title.update"), summary), rail),
		Groups:        e.groups,
		Collection:    e.collection,
		Object:        e.object,
		Todo:          todo,
		Due:           ctx.Dates().Input(at),
		Priority:      calendar.PriorityBand(todo),
		PriorityBands: calendar.PriorityBands,
	})
}
