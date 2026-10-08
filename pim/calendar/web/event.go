package web

import (
	"fmt"
	"net/http"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/dates"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim/calendar"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/search"
	"git.mehdix.org/alborz/web"
	"github.com/alborzmail/go-itip"
	"github.com/emersion/go-ical"
)

var eventListParams = []string{"account", "cal", "month", "date", "view", "span", "query"}

type EventRenderData struct {
	CalendarObjectRenderData
	Event calendar.Object
	// Authors are who added and last changed the object, where another account.
	// List is the view the page was opened from.
	Star string
	// Repeats says the rule in words; RepeatsUntil its last day.
	Repeats, RepeatsUntil string
}

type UpdateEventRenderData struct {
	UpdateCalendarObjectRenderData
	Event *ical.Event

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
	case ctx.QueryParam("month") != "" && ctx.QueryParam("view") != "":
		mv, err := p.monthView(ctx)
		if err != nil {
			return nil, err
		}
		shown = mv.shown()
	// A list with no month is a search across all time.
	case ctx.QueryParam("query") != "" || ctx.QueryParam("view") != "":
		found, err := p.eventSearch(ctx, pimweb.Only(ctx, "cal"), ctx.QueryParam("view"), search.Parse(ctx.QueryParam("query")))
		if err != nil {
			return nil, err
		}
		shown = found.Found
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
	e, err := p.openCalendarObject(ctx, path, "notfound.event", "failed to get event", alborz.NotFound("notfound.event"), calendar.FirstEvent)
	if err != nil {
		return err
	}
	event := e.object
	vevent := e.component
	summary, _ := vevent.Props.Text("SUMMARY")

	rail, err := p.eventRail(ctx)
	if err != nil {
		return err
	}
	items, err := p.eventItems(ctx)
	if err != nil {
		return err
	}
	data := &EventRenderData{
		PageData:   *web.NewPageData(ctx, summary, rail).WithItem(),
		List:       eventList(ctx),
		Collection: e.collection,
		Event:      calendar.Object{CalendarObject: event, ReadOnly: !e.collection.Writable},
		Star:       calendar.ComponentColor(vevent),
		Neighbours: pimweb.Around(items, path),
		Authors:    p.DAV.Authors(event.Path, ctx.Login.Username()),
	}
	data.Repeats, data.RepeatsUntil = repeatWords(ctx, &ical.Event{Component: vevent}, web.UserLocation(ctx))
	return ctx.Render(http.StatusOK, "calendar/event", data)
}

func (p *plugin) updateEvent(ctx *web.Context) error {
	calendarObjectPath := ctx.Param("path")
	loc := web.UserLocation(ctx)
	e, err := p.editCalendarObject(ctx, calendarObjectPath, "notfound.event", "failed to get CalDAV event", alborz.NotFound("notfound.event"), calendar.SupportsEvent,
		func(cal *ical.Calendar) *ical.Component {
			// The VEVENT without RECURRENCE-ID is the series; the form edits that. An invitation to single
			// occurrences carries no series (RFC 5546 4.4.4), and the form edits the one the event's page shows.
			events := cal.Events()
			if len(events) == 0 {
				return nil
			}
			event := &events[0]
			for i := range events {
				if events[i].Props.Get(ical.PropRecurrenceID) == nil {
					event = &events[i]
				}
			}
			return event.Component
		}, func() *ical.Component {
			// Pooled, so creating does not fail when the active account lacks such a collection.
			event := ical.NewEvent()
			event.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
			return event.Component
		})
	if err != nil {
		return err
	}
	event := &ical.Event{Component: e.component}
	var before *ical.Calendar
	if !e.creating() {
		before = calendar.Clone(e.object.Data)
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
				PageData:    *web.NewPageData(ctx, ctx.T("calendar.createtitle"), rail).WithRefused(message),
				Groups:      e.groups,
				Collection:  e.collection,
				Object:      e.object,
				Event:       event,
				AllDay:      ctx.FormValue("allday") != "",
				StartTime:   ctx.DateField("start"),
				EndTime:     ctx.DateField("end"),
				StartDate:   ctx.DateField("start_date"),
				EndDate:     ctx.DateField("end_date"),
				Repeat:      calendar.Repeat{Freq: ctx.FormValue("repeat"), Until: ctx.DateField("until")},
				RepeatFreqs: calendar.RepeatFreqs,
			})
		}
		if summary == "" {
			return reject(ctx.T("form.summaryneeded"))
		}

		to, err := e.destination(ctx, p, calendar.SupportsEvent)
		if handled, refusal := pimweb.DestinationRefusal(ctx, err, reject); handled {
			return refusal
		}
		if err != nil {
			return err
		}
		creating := e.creating()
		if event.Props.Get(ical.PropUID) == nil {
			event.Props.SetText(ical.PropUID, uuid.New().String())
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
			if start.Equal(end) {
				end = start.Add(24 * time.Hour)
			}
		}

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

		setDescription(event.Component, description, true)

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

		cal, err := e.put(ctx, to, event.Component, start)
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
			schedules, err = p.ServerSchedules(ctx.Request().Context(), ctx.LoginFor(to.Account), e.object.Path)
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

		return pimweb.Saved(ctx, creating, ctx.T("notice.eventcreated"), summary, ctx.Path("calendar.event", e.object.Path), ctx.Path("calendar.month"), to.Account)
	}

	summary, _ := event.Props.Text("SUMMARY")

	rail, err := p.eventRail(ctx)
	if err != nil {
		return err
	}
	data := &UpdateEventRenderData{
		PageData:   *web.NewPageData(ctx, fmt.Sprintf(ctx.T("title.update"), summary), rail),
		Groups:     e.groups,
		Collection: e.collection,
		Object:     e.object,
		Event:      event,
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
