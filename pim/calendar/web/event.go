package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/dates"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/alborzmail/go-itip"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

var eventListParams = []string{"account", "cal", "month", "date", "view", "span"}

type EventRenderData struct {
	web.BaseRenderData
	Rail     pimweb.Rail
	Calendar *dav.Collection
	Event    calendar.CalendarObject
	// Authors are who added and last changed the object, where another account.
	Authors dav.Authors
	// List is the view the page was opened from.
	List       string
	Star       string
	Neighbours web.Neighbours
	// Repeats says the rule in words; RepeatsUntil its last day.
	Repeats, RepeatsUntil string
}

type UpdateEventRenderData struct {
	web.BaseRenderData
	Rail           pimweb.Rail
	Groups         []dav.Group
	Calendar       *dav.Collection
	CalendarObject *caldav.CalendarObject
	Event          *ical.Event

	// Both shapes are filled, so ticking the box loses nothing typed.
	AllDay    bool
	StartTime dates.Field
	EndTime   dates.Field
	StartDate dates.Field
	EndDate   dates.Field
	// Attendees are the invited addresses, one per line.
	Attendees string
	Repeat    calendar.Repeat
	// RepeatFreqs are the rules the form offers, in its order.
	RepeatFreqs []string
}

func newEventStart(ctx *web.Context, loc *time.Location) time.Time {
	now := time.Now().In(loc)
	d, err := time.ParseInLocation(datePageLayout, ctx.QueryParam("date"), loc)
	if err != nil {
		return now
	}
	return time.Date(d.Year(), d.Month(), d.Day(), now.Hour(), now.Minute(), 0, 0, loc)
}

func fillEventForm(ctx *web.Context, d *UpdateEventRenderData, loc *time.Location, when time.Time) {
	d.RepeatFreqs = calendar.RepeatFreqs
	d.Repeat = calendar.FormRepeat(ctx.Dates(), d.Event, loc)
	start, _ := d.Event.DateTimeStart(loc)
	end, _ := d.Event.DateTimeEnd(loc)
	if prop := d.Event.Props.Get(ical.PropDateTimeStart); prop != nil {
		d.AllDay = prop.ValueType() == ical.ValueDate
	}
	if start.IsZero() {
		start = when
	}
	if !end.After(start) {
		end = start.Add(time.Hour)
	}
	d.StartTime = d.GlobalData.InputDateTime(start)
	d.EndTime = d.GlobalData.InputDateTime(end)
	d.StartDate = d.GlobalData.InputDate(start)
	last := end
	if d.AllDay {
		last = end.AddDate(0, 0, -1)
		if last.Before(start) {
			last = start
		}
	}
	d.EndDate = d.GlobalData.InputDate(last)
	d.Attendees = calendar.AttendeeLines(d.Event)
}

func dayQuery(ctx *web.Context, start time.Time) string {
	q := pimweb.ListParams(ctx, "account", "cal")
	q.Set("date", start.Format(datePageLayout))
	return web.Query(q)
}

func monthQuery(ctx *web.Context, mv monthView) string {
	q := pimweb.ListParams(ctx, "account", "cal", "span", "group")
	q.Set("month", mv.page)
	if mv.view != "" {
		q.Set("view", mv.view)
	}
	return web.Query(q)
}

func (p *plugin) eventItems(ctx *web.Context) ([]pimweb.Item, error) {
	var shown []calendar.Occurrence
	switch {
	case ctx.QueryParam("date") != "":
		dv, err := p.dayView(ctx)
		if err != nil {
			return nil, err
		}
		shown = dv.events
	case ctx.QueryParam("view") != "":
		mv, err := p.monthView(ctx)
		if err != nil {
			return nil, err
		}
		shown = mv.shown()
	default:
		return nil, nil
	}

	params := pimweb.RowParams(ctx, "", pimweb.ListParams(ctx, eventListParams...))
	var items []pimweb.Item
	seen := map[string]bool{}
	for _, oc := range shown {
		if seen[oc.Path] {
			continue
		}
		seen[oc.Path] = true
		items = append(items, pimweb.Item{Path: oc.Path, URL: pimweb.ObjectURL(ctx, "calendar.event", oc.Path, oc.Account, params)})
	}
	return items, nil
}

func (p *plugin) event(ctx *web.Context) error {
	path := ctx.Param("path")

	c, calendars, err := p.Open(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	coll := dav.Holding(calendars, "", path)
	if coll == nil {
		return alborz.NotFound("notfound.event")
	}

	multiGet := caldav.CalendarMultiGet{
		CompRequest: caldav.CalendarCompRequest{
			Name:  "VCALENDAR",
			Props: []string{"VERSION"},
			// Everything, since the page draws alarms and attendees.
			Comps: []caldav.CalendarCompRequest{{Name: "VEVENT", AllProps: true, AllComps: true}},
		},
	}

	events, err := c.MultiGetCalendar(ctx.Request().Context(), path, &multiGet)
	if err != nil {
		if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
			return alborz.NotFound("notfound.event")
		}
		return fmt.Errorf("failed to multi-get calendar: %v", err)
	}
	if len(events) == 0 {
		return alborz.NotFound("notfound.event")
	}
	if len(events) != 1 {
		return fmt.Errorf("expected exactly one calendar object with path %q, got %v", path, len(events))
	}
	event := &events[0]
	vevents := event.Data.Events()
	if len(vevents) == 0 {
		return alborz.NotFound("notfound.event")
	}
	summary, _ := vevents[0].Props.Text("SUMMARY")

	rail, err := p.eventRail(ctx)
	if err != nil {
		return err
	}
	items, err := p.eventItems(ctx)
	if err != nil {
		return err
	}
	data := &EventRenderData{
		Rail:           rail,
		List:           eventList(ctx),
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(summary).WithItem(),
		Calendar:       coll,
		Event:          calendar.CalendarObject{CalendarObject: event, ReadOnly: !coll.Writable},
		Star:           calendar.ComponentColor(vevents[0].Component),
		Neighbours:     pimweb.Around(items, path),
		Authors:        p.DAV.Authors(event.Path, ctx.Login.Username()),
	}
	data.Repeats, data.RepeatsUntil = repeatWords(ctx, &vevents[0], web.UserLocation(ctx))
	return ctx.Render(http.StatusOK, "calendar/event", data)
}

func (p *plugin) updateEvent(ctx *web.Context) error {
	calendarObjectPath := ctx.Param("path")

	loc := web.UserLocation(ctx)

	var err error
	var c *caldav.Client
	var calendars []dav.Collection
	var groups []dav.Group
	var co *caldav.CalendarObject
	var event *ical.Event
	var before *ical.Calendar
	var currentCalendar *dav.Collection
	if calendarObjectPath != "" {
		c, calendars, err = p.Open(ctx.Request().Context(), ctx.Login)
		if err != nil {
			return err
		}
		co, err = c.GetCalendarObject(ctx.Request().Context(), calendarObjectPath)
		if err != nil {
			if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
				return alborz.NotFound("notfound.event")
			}
			return fmt.Errorf("failed to get CalDAV event: %v", err)
		}
		// The VEVENT without RECURRENCE-ID is the series; the form edits that.
		events := co.Data.Events()
		for i := range events {
			if events[i].Props.Get(ical.PropRecurrenceID) == nil {
				event = &events[i]
			}
		}
		if event == nil {
			return fmt.Errorf("calendar object %q holds no event to edit", calendarObjectPath)
		}
		before = calendar.Clone(co.Data)
		currentCalendar = dav.Holding(calendars, "", co.Path)
	} else {
		// Pooled, so creating does not fail when the active account lacks such a collection.
		groups, err = p.writableGroups(ctx, calendar.SupportsEvent)
		if err != nil {
			return err
		}
		event = ical.NewEvent()
		event.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
		currentCalendar = &dav.Collection{}
		if len(groups) > 0 {
			currentCalendar = &groups[0].Collections[0]
		}
	}

	if ctx.Request().Method == "POST" {
		summary := ctx.FormValue("summary")
		description := ctx.FormValue("description")

		// Invalid input re-renders the form with an alert, never a status page.
		reject := func(message string) error {
			rail, err := p.eventRail(ctx)
			if err != nil {
				return err
			}
			return ctx.Render(http.StatusUnprocessableEntity, "calendar/event-edit", &UpdateEventRenderData{
				Rail:           rail,
				BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("calendar.createtitle")).Refused(message),
				Groups:         groups,
				Calendar:       currentCalendar,
				CalendarObject: co,
				Event:          event,
				AllDay:         ctx.FormValue("allday") != "",
				StartTime:      ctx.DateField("start"),
				EndTime:        ctx.DateField("end"),
				StartDate:      ctx.DateField("start_date"),
				EndDate:        ctx.DateField("end_date"),
				Repeat:         calendar.Repeat{Freq: ctx.FormValue("repeat"), Until: ctx.DateField("until")},
				RepeatFreqs:    calendar.RepeatFreqs,
			})
		}
		if summary == "" {
			return reject(ctx.T("form.summaryneeded"))
		}

		to := pim.Ref[*caldav.Client]{Client: c, Account: ctx.Login.Username()}
		creating := co == nil
		if creating {
			to, err = p.destination(ctx, ctx.FormValue("calendar"), calendar.SupportsEvent)
			if errors.Is(err, pim.ErrNoDestination) {
				return reject(ctx.T("form.destinationneeded"))
			} else if err != nil {
				return err
			}
			for _, g := range groups {
				if g.Account == to.Account {
					currentCalendar = dav.At(g.Collections, to.Path)
				}
			}
		}

		allDay := ctx.FormValue("allday") != ""
		var start, end time.Time
		if allDay {
			start, err = ctx.DateField("start_date").Read(loc)
			if err != nil {
				return reject(ctx.T("form.datesneeded"))
			}
			end, err = ctx.DateField("end_date").Read(loc)
			if err != nil {
				end = start
			}
			if start.After(end) {
				return reject(ctx.T("form.endbeforestart"))
			}
			// DTEND is exclusive; the form asks for the last day.
			end = end.AddDate(0, 0, 1)
		} else {
			start, err = ctx.DateField("start").ReadTime(loc)
			if err != nil {
				return reject(ctx.T("form.datesneeded"))
			}
			end, err = ctx.DateField("end").ReadTime(loc)
			if err != nil {
				return reject(ctx.T("form.datesneeded"))
			}
			if start.After(end) {
				return reject(ctx.T("form.endbeforestart"))
			}
			if start == end {
				end = start.Add(24 * time.Hour)
			}
		}

		event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
		event.Props.SetText(ical.PropSummary, summary)
		if allDay {
			event.Props.SetDate(ical.PropDateTimeStart, start)
			event.Props.SetDate(ical.PropDateTimeEnd, end)
		} else {
			event.Props.SetDateTime(ical.PropDateTimeStart, start)
			event.Props.SetDateTime(ical.PropDateTimeEnd, end)
		}
		event.Props.Del(ical.PropDuration)
		if err := calendar.SetRepeat(ctx.Dates(), event, calendar.Repeat{Freq: ctx.FormValue("repeat"), Until: ctx.DateField("until")}, start, allDay, loc); err != nil {
			return reject(ctx.T("form.repeatuntil"))
		}

		if description != "" {
			description = strings.ReplaceAll(description, "\r", "")
			event.Props.SetText(ical.PropDescription, description)
		} else {
			event.Props.Del(ical.PropDescription)
		}

		// Naming anybody makes this account the organizer; removing the last withdraws it.
		attendees := calendar.ParseAttendees(ctx.FormValue("attendees"))
		calendar.SetScheduling(event, ctx.Login.Username(), attendees)
		var was *ical.Component
		if before != nil {
			was = itip.Series(before)
		}
		dropped := itip.Dropped(was, event.Component)
		if len(attendees) > 0 || len(dropped) > 0 {
			itip.Revise(was, event.Component)
		}

		newID := uuid.New()
		if prop := event.Props.Get(ical.PropUID); prop == nil {
			event.Props.SetText(ical.PropUID, newID.String())
		}

		cal := calendar.NewCalendar(event.Component)
		if !creating {
			cal = co.Data
		}
		calendar.EnsureTimezones(cal, start)
		co, err = calendar.PutObject(ctx.Request().Context(), to, newID.String()+".ics", co, cal)
		if err != nil {
			return reject(pimweb.Refusal(ctx, err))
		}

		// Saved before sending: a failed send must not lose the event.
		ours, err := mail.Addresses(ctx.Request().Context(), ctx.Login)
		if err != nil {
			return err
		}
		told := calendar.Others(attendees, ours)
		schedules := false
		if len(told) > 0 || len(dropped) > 0 {
			schedules, err = p.ServerSchedules(ctx.Request().Context(), ctx.LoginFor(to.Account), co.Path)
		}
		if err == nil && !schedules {
			err = sendScheduling(ctx, itip.Request(cal), told)
		}
		if len(dropped) > 0 && err == nil && !schedules {
			err = sendScheduling(ctx, itip.Cancel(before, dropped, time.Now()), calendar.Others(dropped, ours))
		}
		if err != nil {
			ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.T("invite.sendfailed")})
			ctx.Logger().Printf("failed to send the scheduling message: %v", err)
		} else if !schedules && (len(told) > 0 || len(dropped) > 0) {
			ctx.PutNotice(ctx.T("invite.sent"))
		}

		return pimweb.Saved(ctx, creating, ctx.T("notice.eventcreated"), summary, ctx.Path("calendar.event", co.Path), ctx.Path("calendar.month"), to.Account)
	}

	summary, _ := event.Props.Text("SUMMARY")

	rail, err := p.eventRail(ctx)
	if err != nil {
		return err
	}
	data := &UpdateEventRenderData{
		Rail:           rail,
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("title.update"), summary)),
		Groups:         groups,
		Calendar:       currentCalendar,
		CalendarObject: co,
		Event:          event,
	}
	fillEventForm(ctx, data, loc, newEventStart(ctx, loc))
	return ctx.Render(http.StatusOK, "calendar/event-edit", data)
}

func eventList(ctx *web.Context) string {
	if from := ctx.From(); from != "" {
		return from
	}
	params := pimweb.ListParams(ctx, eventListParams...)
	if params.Get("date") != "" {
		return pimweb.ListURL(ctx.Path("calendar.day"), params)
	}
	return pimweb.ListURL(ctx.Path("calendar.month"), params)
}

func repeatWords(ctx *web.Context, event *ical.Event, loc *time.Location) (words, until string) {
	rep := calendar.FormRepeat(ctx.Dates(), event, loc)
	switch rep.Freq {
	case "":
		return "", ""
	case calendar.RepeatCustom:
		return ctx.T("calendar.repeat.custom"), ""
	}
	words = ctx.T("calendar.repeat." + rep.Freq)
	if rep.Until.Value != "" {
		return fmt.Sprintf(ctx.T("calendar.repeat.until"), words), rep.Until.Value
	}
	return words, ""
}
