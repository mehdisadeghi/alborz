package alborzcaldav

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
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/labstack/echo/v4"
)

type TaskRenderData struct {
	alborz.BaseRenderData
	Rail     dav.Rail
	Calendar *dav.Collection
	Task     TaskObject
	// Authors are who added the object and changed it last, where it is
	// kept here and that is another account.
	Authors dav.Authors
	// List is the list the page was opened from, filter and order kept,
	// for what leaves the page with nothing to come back to.
	List       string
	Star       string
	Priority   string
	Neighbours dav.Neighbours
}

type UpdateTaskRenderData struct {
	alborz.BaseRenderData
	Rail           dav.Rail
	Groups         []dav.Group
	Calendar       *dav.Collection
	CalendarObject *caldav.CalendarObject
	Todo           *ical.Component
	// Due is the due date as the field holds it: what was typed, or
	// what the task has.
	Due string
	// Priority is the band the select holds, and PriorityBands the
	// bands it offers, in order.
	Priority      string
	PriorityBands []string
	Error         string
}

// The bands RFC 5545 3.8.1.9 draws over PRIORITY: 1 to 4 high, 5
// medium, 6 to 9 low, 0 or absent none. Apple writes 1, 5 and 9.
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

// setPriorityBand writes a band as Apple does, and leaves a number
// another client wrote alone while it still falls in the band chosen.
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
	path, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	c, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
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
			Comps: []caldav.CalendarCompRequest{{
				Name: "VTODO",
				Props: []string{
					"SUMMARY",
					"DESCRIPTION",
					"UID",
					"DUE",
					"STATUS",
					"COLOR",
				},
			}},
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
	// The list the page was opened from is rebuilt to find what stands
	// either side; the DAV reads behind it are cached, so the cost is
	// the sorting, not another round trip.
	list, err := p.taskList(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "task.html", &TaskRenderData{
		Rail:           rail,
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(summary),
		Calendar:       calendar,
		Task:           TaskObject{CalendarObject: task},
		List:           cmp.Or(ctx.From(), dav.ListURL("/tasks", dav.ListParams(ctx, taskListParams...))),
		Star:           componentColor(getFirstTodo(task.Data)),
		Priority:       priorityBand(getFirstTodo(task.Data)),
		Neighbours:     dav.Around(list.Items, path),
		Authors:        p.dav.Authors(task.Path, ctx.Session.Username()),
	})
}
func (p *plugin) updateTask(ctx *alborz.Context) error {
	taskPath, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	loc := alborzbase.UserLocation(ctx)

	var c *caldav.Client
	var calendars []dav.Collection
	var groups []dav.Group
	var co *caldav.CalendarObject
	var todo *ical.Component
	var currentCalendar *dav.Collection
	if taskPath != "" {
		c, calendars, err = p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}
		co, err = getCalendarObject(ctx, c, taskPath)
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
		if len(groups) == 0 || len(groups[0].Collections) == 0 {
			return alborz.RenderInfo(ctx, http.StatusOK, ctx.T("calendar.nowritable"))
		}
		todo = ical.NewComponent(ical.CompToDo)
		todo.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
		currentCalendar = &groups[0].Collections[0]
	}

	if ctx.Request().Method == "POST" {
		summary := ctx.FormValue("summary")
		description := ctx.FormValue("description")
		dueDate := ctx.FormValue("due-date")
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		// A form without the select, the line above the list, says
		// nothing about priority and changes nothing.
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
				BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("tasks.createtitle")),
				Groups:         groups,
				Calendar:       currentCalendar,
				CalendarObject: co,
				Todo:           todo,
				Due:            dueDate,
				Priority:       band,
				PriorityBands:  priorityBands,
				Error:          message,
			})
		}
		if summary == "" {
			return reject(ctx.T("form.summaryneeded"))
		}

		to := dav.Ref[*caldav.Client]{Client: c}
		creating := co == nil
		if creating {
			to, err = p.destination(ctx, ctx.FormValue("calendar"), supportsTodo)
			if errors.Is(err, dav.ErrNoDestination) {
				return reject(ctx.T("form.destinationneeded"))
			} else if err != nil {
				return err
			}
		}

		todo.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
		todo.Props.SetText(ical.PropSummary, summary)

		if description != "" {
			description = strings.ReplaceAll(description, "\r", "")
			todo.Props.SetText(ical.PropDescription, description)
		} else {
			todo.Props.Del(ical.PropDescription)
		}

		// The zone definition has to cover the date it qualifies,
		// so a dated task is bracketed by its own due date.
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

		return dav.Saved(ctx, creating, ctx.T("notice.taskcreated"), summary, TaskObject{CalendarObject: co}.URL(), "/tasks", to.Account)
	}

	summary, _ := todo.Props.Text("SUMMARY")
	var due string
	if prop := todo.Props.Get(ical.PropDue); prop != nil {
		at, err := prop.DateTime(loc)
		if err != nil {
			return err
		}
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
