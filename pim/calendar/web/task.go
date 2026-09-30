package web

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/dates"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

type TaskRenderData struct {
	web.BaseRenderData
	Rail     pimweb.Rail
	Calendar *dav.Collection
	Task     calendar.TaskObject
	// Authors are who added and last changed the object, where another account.
	Authors dav.Authors
	// List is the list the page was opened from.
	List       string
	Star       string
	Priority   string
	Neighbours web.Neighbours
}

type UpdateTaskRenderData struct {
	web.BaseRenderData
	Rail           pimweb.Rail
	Groups         []dav.Group
	Calendar       *dav.Collection
	CalendarObject *caldav.CalendarObject
	Todo           *ical.Component
	// Due is the due date as the field holds it.
	Due dates.Field
	// Priority is the band the select holds; PriorityBands the bands offered.
	Priority      string
	PriorityBands []string
}

func (p *plugin) task(ctx *web.Context) error {
	path := ctx.Param("path")

	c, calendars, err := p.Open(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}

	coll := dav.Holding(calendars, "", path)
	if coll == nil {
		return alborz.NotFound("notfound.task")
	}

	multiGet := caldav.CalendarMultiGet{
		CompRequest: caldav.CalendarCompRequest{
			Name:  "VCALENDAR",
			Props: []string{"VERSION"},
			// The whole to-do: the form reads its priority and dates.
			Comps: []caldav.CalendarCompRequest{{Name: "VTODO", AllProps: true, AllComps: true}},
		},
	}

	tasks, err := c.MultiGetCalendar(ctx.Request().Context(), path, &multiGet)
	if err != nil {
		if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
			return alborz.NotFound("notfound.task")
		}
		return fmt.Errorf("failed to get task: %v", err)
	}
	if len(tasks) == 0 {
		return alborz.NotFound("notfound.task")
	}
	if len(tasks) != 1 {
		return fmt.Errorf("expected exactly one task with path %q, got %v", path, len(tasks))
	}
	task := &tasks[0]
	if task.Data == nil {
		return web.NewError(http.StatusBadGateway, ctx.T("calendar.unreadable"))
	}
	todo := calendar.FirstTodo(task.Data)
	if todo == nil {
		return fmt.Errorf("no VTODO component found")
	}
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
		Rail:           rail,
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(summary).WithItem(),
		Calendar:       coll,
		Task:           calendar.TaskObject{CalendarObject: task},
		List:           cmp.Or(ctx.From(), pimweb.ListURL(ctx.Path("calendar.tasks"), pimweb.ListParams(ctx, taskListParams...))),
		Star:           calendar.ComponentColor(calendar.FirstTodo(task.Data)),
		Priority:       calendar.PriorityBand(calendar.FirstTodo(task.Data)),
		Neighbours:     pimweb.Around(list.Items, path),
		Authors:        p.DAV.Authors(task.Path, ctx.Login.Username()),
	})
}
func (p *plugin) updateTask(ctx *web.Context) error {
	taskPath := ctx.Param("path")

	loc := web.UserLocation(ctx)

	var err error
	var c *caldav.Client
	var calendars []dav.Collection
	var groups []dav.Group
	var co *caldav.CalendarObject
	var todo *ical.Component
	var currentCalendar *dav.Collection
	if taskPath != "" {
		c, calendars, err = p.Open(ctx.Request().Context(), ctx.Login)
		if err != nil {
			return err
		}
		co, err = c.GetCalendarObject(ctx.Request().Context(), taskPath)
		if err != nil {
			if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
				return alborz.NotFound("notfound.task")
			}
			return fmt.Errorf("failed to get task: %v", err)
		}
		todo = calendar.FirstTodo(co.Data)
		if todo == nil {
			return fmt.Errorf("no VTODO component found")
		}
		currentCalendar = dav.Holding(calendars, "", co.Path)
	} else {
		groups, err = p.writableGroups(ctx, calendar.SupportsTodo)
		if err != nil {
			return err
		}
		todo = ical.NewComponent(ical.CompToDo)
		todo.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
		currentCalendar = &dav.Collection{}
		if len(groups) > 0 {
			currentCalendar = &groups[0].Collections[0]
		}
	}

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
				Rail:           rail,
				BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("tasks.createtitle")).Refused(message),
				Groups:         groups,
				Calendar:       currentCalendar,
				CalendarObject: co,
				Todo:           todo,
				Due:            dueDate,
				Priority:       band,
				PriorityBands:  calendar.PriorityBands,
			})
		}
		if summary == "" {
			return reject(ctx.T("form.summaryneeded"))
		}

		to := pim.Ref[*caldav.Client]{Client: c}
		creating := co == nil
		if creating {
			to, err = p.destination(ctx, ctx.FormValue("calendar"), calendar.SupportsTodo)
			if errors.Is(err, pim.ErrNoDestination) {
				return reject(ctx.T("form.destinationneeded"))
			} else if err != nil {
				return err
			}
		}

		todo.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
		todo.Props.SetDateTime(ical.PropLastModified, time.Now().UTC())
		todo.Props.SetText(ical.PropSummary, summary)

		if description != "" {
			description = strings.ReplaceAll(description, "\r", "")
			todo.Props.SetText(ical.PropDescription, description)
		} else {
			todo.Props.Del(ical.PropDescription)
		}

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

		newID := uuid.New()
		if prop := todo.Props.Get(ical.PropUID); prop == nil {
			todo.Props.SetText(ical.PropUID, newID.String())
			todo.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
		}

		cal := calendar.NewCalendar(todo)
		if !creating {
			cal = co.Data
		}
		calendar.EnsureTimezones(cal, due)
		co, err = calendar.PutObject(ctx.Request().Context(), to, newID.String()+".ics", co, cal)
		if err != nil {
			return reject(fmt.Sprintf(ctx.T("form.saverefused"), err))
		}

		return pimweb.Saved(ctx, creating, ctx.T("notice.taskcreated"), summary, ctx.Path("calendar.task", co.Path), ctx.Path("calendar.tasks"), to.Account)
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
		Rail:           rail,
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("title.update"), summary)),
		Groups:         groups,
		Calendar:       currentCalendar,
		CalendarObject: co,
		Todo:           todo,
		Due:            ctx.Dates().Input(at),
		Priority:       calendar.PriorityBand(todo),
		PriorityBands:  calendar.PriorityBands,
	})
}
