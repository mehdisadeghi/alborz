package calendar

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/labstack/echo/v4"
)

type TaskRenderData struct {
	alborz.BaseRenderData
	Rail     pim.Rail
	Calendar *dav.Collection
	Task     TaskObject
	// Authors are who added and last changed the object, where another account.
	Authors dav.Authors
	// List is the list the page was opened from.
	List       string
	Star       string
	Priority   string
	Neighbours pim.Neighbours
}

type UpdateTaskRenderData struct {
	alborz.BaseRenderData
	Rail           pim.Rail
	Groups         []dav.Group
	Calendar       *dav.Collection
	CalendarObject *caldav.CalendarObject
	Todo           *ical.Component
	// Due is the due date as the field holds it.
	Due string
	// Priority is the band the select holds; PriorityBands the bands offered.
	Priority      string
	PriorityBands []string
}

// PRIORITY bands (RFC 5545 3.8.1.9): 1-4 high, 5 medium, 6-9 low, 0 none.
const (
	priorityHigh   = "high"
	priorityMedium = "medium"
	priorityLow    = "low"
)

var priorityBands = []string{priorityHigh, priorityMedium, priorityLow}

func priorityBand(todo *ical.Component) string {
	prop := todo.Props.Get(ical.PropPriority)
	if prop == nil {
		return ""
	}
	n, err := strconv.Atoi(prop.Value)
	switch {
	case err != nil || n <= 0:
		return ""
	case n <= 4:
		return priorityHigh
	case n == 5:
		return priorityMedium
	default:
		return priorityLow
	}
}

// A number another client wrote stays while it falls in the chosen band.
func setPriorityBand(todo *ical.Component, band string) {
	if band == priorityBand(todo) {
		return
	}
	value := map[string]string{priorityHigh: "1", priorityMedium: "5", priorityLow: "9"}[band]
	if value == "" {
		todo.Props.Del(ical.PropPriority)
		return
	}
	prop := ical.NewProp(ical.PropPriority)
	prop.Value = value
	todo.Props.Set(prop)
}

func (p *plugin) task(ctx *alborz.Context) error {
	path, err := pim.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	c, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}

	calendar := dav.Holding(calendars, "", path)
	if calendar == nil {
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
	todo := getFirstTodo(task.Data)
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
	return ctx.Render(http.StatusOK, "task.html", &TaskRenderData{
		Rail:           rail,
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(summary).WithItem(),
		Calendar:       calendar,
		Task:           TaskObject{CalendarObject: task},
		List:           cmp.Or(ctx.From(), pim.ListURL("/tasks", pim.ListParams(ctx, taskListParams...))),
		Star:           componentColor(getFirstTodo(task.Data)),
		Priority:       priorityBand(getFirstTodo(task.Data)),
		Neighbours:     pim.Around(list.Items, path),
		Authors:        p.dav.Authors(task.Path, ctx.Login.Username()),
	})
}
func (p *plugin) updateTask(ctx *alborz.Context) error {
	taskPath, err := pim.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	loc := alborz.UserLocation(ctx)

	var c *caldav.Client
	var calendars []dav.Collection
	var groups []dav.Group
	var co *caldav.CalendarObject
	var todo *ical.Component
	var currentCalendar *dav.Collection
	if taskPath != "" {
		c, calendars, err = p.clientWithCalendars(ctx.Request().Context(), ctx.Login)
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
		todo = getFirstTodo(co.Data)
		if todo == nil {
			return fmt.Errorf("no VTODO component found")
		}
		currentCalendar = dav.Holding(calendars, "", co.Path)
	} else {
		groups, err = p.writableGroups(ctx, supportsTodo)
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
		dueDate := ctx.FormValue("due-date")
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		// A form without the select changes no priority.
		_, prioritySent := params["priority"]
		band := ctx.FormValue("priority")
		if band != "" && !slices.Contains(priorityBands, band) {
			return echo.NewHTTPError(http.StatusBadRequest, "no such priority")
		}

		reject := func(message string) error {
			rail, err := p.taskRail(ctx)
			if err != nil {
				return err
			}
			return ctx.Render(http.StatusUnprocessableEntity, "update-task.html", &UpdateTaskRenderData{
				Rail:           rail,
				BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("tasks.createtitle")).Refused(message),
				Groups:         groups,
				Calendar:       currentCalendar,
				CalendarObject: co,
				Todo:           todo,
				Due:            dueDate,
				Priority:       band,
				PriorityBands:  priorityBands,
			})
		}
		if summary == "" {
			return reject(ctx.T("form.summaryneeded"))
		}

		to := pim.Ref[*caldav.Client]{Client: c}
		creating := co == nil
		if creating {
			to, err = p.destination(ctx, ctx.FormValue("calendar"), supportsTodo)
			if errors.Is(err, pim.ErrNoDestination) {
				return reject(ctx.T("form.destinationneeded"))
			} else if err != nil {
				return err
			}
		}

		if creating {
			todo.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
		} else {
			todo.Props.SetDateTime(ical.PropLastModified, time.Now().UTC())
		}
		todo.Props.SetText(ical.PropSummary, summary)

		if description != "" {
			description = strings.ReplaceAll(description, "\r", "")
			todo.Props.SetText(ical.PropDescription, description)
		} else {
			todo.Props.Del(ical.PropDescription)
		}

		// The zone definition must cover the date it qualifies, so it brackets the due date.
		due := time.Now().In(loc)
		if dueDate != "" {
			at, err := ctx.ReadDate(dueDate, loc)
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
			setPriorityBand(todo, band)
		}

		newID := uuid.New()
		if prop := todo.Props.Get(ical.PropUID); prop == nil {
			todo.Props.SetText(ical.PropUID, newID.String())
			todo.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
		}

		cal := newCalendar(todo)
		if !creating {
			cal = co.Data
		}
		ensureTimezones(cal, due)
		co, err = putObject(ctx, to, newID.String()+".ics", co, cal)
		if err != nil {
			return reject(fmt.Sprintf(ctx.T("form.saverefused"), err))
		}

		return pim.Saved(ctx, creating, ctx.T("notice.taskcreated"), summary, TaskObject{CalendarObject: co}.URL(), "/tasks", to.Account)
	}

	summary, _ := todo.Props.Text("SUMMARY")
	// DUE, or DTSTART from a writer that sent only the start.
	var due string
	at, err := todo.Props.DateTime(ical.PropDue, loc)
	if err != nil {
		return err
	}
	if at.IsZero() {
		if at, err = todo.Props.DateTime(ical.PropDateTimeStart, loc); err != nil {
			return err
		}
	}
	if !at.IsZero() {
		due = ctx.InputDate(at)
	}

	rail, err := p.taskRail(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "update-task.html", &UpdateTaskRenderData{
		Rail:           rail,
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("title.update"), summary)),
		Groups:         groups,
		Calendar:       currentCalendar,
		CalendarObject: co,
		Todo:           todo,
		Due:            due,
		Priority:       priorityBand(todo),
		PriorityBands:  priorityBands,
	})
}
