package calendar

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/alborzmail/go-itip"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

var eventListParams = []string{"account", "cal", "month", "date", "view", "span"}

type EventRenderData struct {
	alborz.BaseRenderData
	Rail     pim.Rail
	Calendar *dav.Collection
	Event    CalendarObject
	// Authors are who added and last changed the object, where another account.
	Authors dav.Authors
	// List is the view the page was opened from.
	List       string
	Star       string
	Neighbours pim.Neighbours
	// Repeats says the rule in words; RepeatsUntil its last day.
	Repeats, RepeatsUntil string
}

type UpdateEventRenderData struct {
	alborz.BaseRenderData
	Rail           pim.Rail
	Groups         []dav.Group
	Calendar       *dav.Collection
	CalendarObject *caldav.CalendarObject
	Event          *ical.Event

	// Both shapes are filled, so ticking the box loses nothing typed.
	AllDay    bool
	StartTime string
	EndTime   string
	StartDate string
	EndDate   string
	// Attendees are the invited addresses, one per line.
	Attendees string
	Repeat    Repeat
	// RepeatFreqs are the rules the form offers, in its order.
	RepeatFreqs []string
}

func newEventStart(ctx *alborz.Context, loc *time.Location) time.Time {
	now := time.Now().In(loc)
	d, err := time.ParseInLocation(datePageLayout, ctx.QueryParam("date"), loc)
	if err != nil {
		return now
	}
	return time.Date(d.Year(), d.Month(), d.Day(), now.Hour(), now.Minute(), 0, 0, loc)
}

func fillEventForm(ctx *alborz.Context, d *UpdateEventRenderData, loc *time.Location, when time.Time) {
	d.RepeatFreqs = repeatFreqs
	d.Repeat = formRepeat(ctx, d.Event, loc)
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
	d.Attendees = attendeeLines(d.Event)
}

func dayQuery(ctx *alborz.Context, start time.Time) string {
	q := pim.ListParams(ctx, "account", "cal")
	q.Set("date", start.Format(datePageLayout))
	return alborz.Query(q)
}

func monthQuery(ctx *alborz.Context, mv monthView) string {
	q := pim.ListParams(ctx, "account", "cal", "span", "group")
	q.Set("month", mv.page)
	if mv.view != "" {
		q.Set("view", mv.view)
	}
	return alborz.Query(q)
}

// A feed is one object holding every event, so there the UID tells rows apart.
func eventKey(path, uid string, feed bool) string {
	if feed {
		return path + "#" + uid
	}
	return path
}

func (p *plugin) eventItems(ctx *alborz.Context) ([]pim.Item, error) {
	var shown []Occurrence
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

	params := pim.RowParams(ctx, "", pim.ListParams(ctx, eventListParams...))
	var items []pim.Item
	seen := map[string]bool{}
	for _, oc := range shown {
		uid, _ := oc.Event.Props.Text("UID")
		key := eventKey(oc.Path, uid, oc.ReadOnly)
		if seen[key] {
			continue
		}
		seen[key] = true
		href := pim.ObjectURL("/calendar/", oc.Path, oc.Account, params)
		if oc.ReadOnly {
			href = pim.ObjectURL("/calendar/", oc.Path, oc.Account, withUID(params, uid))
		}
		items = append(items, pim.Item{Path: key, URL: href})
	}
	return items, nil
}

func withUID(params url.Values, uid string) url.Values {
	q := url.Values{"uid": {uid}}
	for key, values := range params {
		q[key] = values
	}
	return q
}

func (p *plugin) event(ctx *alborz.Context) error {
	path, err := pim.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	c, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	if strings.Contains(path, "://") {
		return p.feedEvent(ctx, path)
	}

	calendar := dav.Holding(calendars, "", path)
	if calendar == nil {
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
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(summary).WithItem(),
		Calendar:       calendar,
		Event:          CalendarObject{CalendarObject: event},
		Star:           componentColor(vevents[0].Component),
		Neighbours:     pim.Around(items, eventKey(path, "", false)),
		Authors:        p.dav.Authors(event.Path, ctx.Login.Username()),
	}
	data.Repeats, data.RepeatsUntil = repeatWords(ctx, &vevents[0], alborz.UserLocation(ctx))
	return ctx.Render(http.StatusOK, "event.html", data)
}

func (p *plugin) feedEvent(ctx *alborz.Context, address string) error {
	info, cal, err := p.feedObject(ctx, address, ctx.QueryParam("uid"))
	if err != nil {
		return err
	}
	event := cal.Events()[0]
	summary, _ := event.Props.Text(ical.PropSummary)
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
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(summary).WithItem(),
		Calendar:       info,
		Event:          CalendarObject{CalendarObject: &caldav.CalendarObject{Path: address, Data: cal}, Color: info.Color, ReadOnly: true},
		Neighbours:     pim.Around(items, eventKey(address, ctx.QueryParam("uid"), true)),
	}
	data.Repeats, data.RepeatsUntil = repeatWords(ctx, &event, alborz.UserLocation(ctx))
	return ctx.Render(http.StatusOK, "event.html", data)
}

func (p *plugin) feedObject(ctx *alborz.Context, address, uid string) (*dav.Collection, *ical.Calendar, error) {
	settings, err := loadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return nil, nil, err
	}
	var info *dav.Collection
	for _, s := range settings.Subscriptions {
		if s.URL == address {
			i := s.info(ctx.Login.Username())
			info = &i
		}
	}
	if info == nil {
		return nil, nil, alborz.NotFound("notfound.subscription", address)
	}
	feed, ok := p.subs.events(ctx.Server.Outgoing.Remote(alborz.RoundTripTimeout), address, info.Color)
	if !ok {
		return nil, nil, alborz.NotFound("notfound.feed", address)
	}
	cal := ical.NewCalendar()
	cal.Props = feed.Data.Props
	for _, ch := range feed.Data.Children {
		id, _ := ch.Props.Text(ical.PropUID)
		if ch.Name == ical.CompTimezone || (ch.Name == ical.CompEvent && id == uid) {
			cal.Children = append(cal.Children, ch)
		}
	}
	if len(cal.Events()) == 0 {
		return nil, nil, alborz.NotFound("notfound.event")
	}
	return info, cal, nil
}

func (p *plugin) updateEvent(ctx *alborz.Context) error {
	calendarObjectPath, err := pim.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	loc := alborz.UserLocation(ctx)

	var c *caldav.Client
	var calendars []dav.Collection
	var groups []dav.Group
	var co *caldav.CalendarObject
	var event *ical.Event
	var before *ical.Calendar
	var currentCalendar *dav.Collection
	if calendarObjectPath != "" {
		c, calendars, err = p.clientWithCalendars(ctx.Request().Context(), ctx.Login)
		if err != nil {
			return err
		}
		co, err = getCalendarObject(ctx, c, calendarObjectPath)
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
		before = cloneCalendar(co.Data)
		currentCalendar = dav.Holding(calendars, "", co.Path)
	} else {
		// Pooled, so creating does not fail when the active account lacks such a collection.
		groups, err = p.writableGroups(ctx, supportsEvent)
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
			return ctx.Render(http.StatusUnprocessableEntity, "update-event.html", &UpdateEventRenderData{
				Rail:           rail,
				BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("calendar.createtitle")).Refused(message),
				Groups:         groups,
				Calendar:       currentCalendar,
				CalendarObject: co,
				Event:          event,
				AllDay:         ctx.FormValue("allday") != "",
				StartTime:      ctx.FormValue("start"),
				EndTime:        ctx.FormValue("end"),
				StartDate:      ctx.FormValue("start_date"),
				EndDate:        ctx.FormValue("end_date"),
				Repeat:         Repeat{Freq: ctx.FormValue("repeat"), Until: ctx.FormValue("until")},
				RepeatFreqs:    repeatFreqs,
			})
		}
		if summary == "" {
			return reject(ctx.T("form.summaryneeded"))
		}

		to := pim.Ref[*caldav.Client]{Client: c, Account: ctx.Login.Username()}
		creating := co == nil
		if creating {
			to, err = p.destination(ctx, ctx.FormValue("calendar"), supportsEvent)
			if errors.Is(err, pim.ErrNoDestination) {
				return reject(ctx.T("form.destinationneeded"))
			} else if err != nil {
				return err
			}
		}

		allDay := ctx.FormValue("allday") != ""
		var start, end time.Time
		if allDay {
			start, err = ctx.ReadDate(ctx.FormValue("start_date"), loc)
			if err != nil {
				return reject(ctx.T("form.datesneeded"))
			}
			end, err = ctx.ReadDate(ctx.FormValue("end_date"), loc)
			if err != nil {
				end = start
			}
			if start.After(end) {
				return reject(ctx.T("form.endbeforestart"))
			}
			// DTEND is exclusive; the form asks for the last day.
			end = end.AddDate(0, 0, 1)
		} else {
			start, err = ctx.ReadDateTime(ctx.FormValue("start"), loc)
			if err != nil {
				return reject(ctx.T("form.datesneeded"))
			}
			end, err = ctx.ReadDateTime(ctx.FormValue("end"), loc)
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
		if err := setRepeat(ctx, event, Repeat{Freq: ctx.FormValue("repeat"), Until: ctx.FormValue("until")}, start, allDay, loc); err != nil {
			return reject(ctx.T("form.repeatuntil"))
		}

		if description != "" {
			description = strings.ReplaceAll(description, "\r", "")
			event.Props.SetText(ical.PropDescription, description)
		} else {
			event.Props.Del(ical.PropDescription)
		}

		// Naming anybody makes this account the organizer; removing the last withdraws it.
		attendees := parseAttendees(ctx.FormValue("attendees"))
		setScheduling(event, ctx.Login.Username(), attendees)
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

		cal := newCalendar(event.Component)
		if !creating {
			cal = co.Data
		}
		ensureTimezones(cal, start)
		co, err = putObject(ctx, to, newID.String()+".ics", co, cal)
		if err != nil {
			return reject(fmt.Sprintf(ctx.T("form.saverefused"), err))
		}

		// Saved before sending: a failed send must not lose the event.
		ours, err := mail.Addresses(ctx.Request().Context(), ctx.Login)
		if err != nil {
			return err
		}
		told := others(attendees, ours)
		schedules := false
		if len(told) > 0 || len(dropped) > 0 {
			schedules, err = p.serverSchedules(ctx, to.Account, co.Path)
		}
		if err == nil && !schedules {
			err = sendScheduling(ctx, itip.Request(cal), told)
		}
		if len(dropped) > 0 && err == nil && !schedules {
			err = sendScheduling(ctx, itip.Cancel(before, dropped, time.Now()), others(dropped, ours))
		}
		if err != nil {
			ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.T("invite.sendfailed")})
			ctx.Logger().Printf("failed to send the scheduling message: %v", err)
		} else if !schedules && (len(told) > 0 || len(dropped) > 0) {
			ctx.PutNotice(ctx.T("invite.sent"))
		}

		return pim.Saved(ctx, creating, ctx.T("notice.eventcreated"), summary, CalendarObject{CalendarObject: co}.URL(), "/calendar", to.Account)
	}

	summary, _ := event.Props.Text("SUMMARY")

	rail, err := p.eventRail(ctx)
	if err != nil {
		return err
	}
	data := &UpdateEventRenderData{
		Rail:           rail,
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(fmt.Sprintf(ctx.T("title.update"), summary)),
		Groups:         groups,
		Calendar:       currentCalendar,
		CalendarObject: co,
		Event:          event,
	}
	fillEventForm(ctx, data, loc, newEventStart(ctx, loc))
	return ctx.Render(http.StatusOK, "update-event.html", data)
}

func eventList(ctx *alborz.Context) string {
	if from := ctx.From(); from != "" {
		return from
	}
	params := pim.ListParams(ctx, eventListParams...)
	if params.Get("date") != "" {
		return pim.ListURL("/calendar/date", params)
	}
	return pim.ListURL("/calendar", params)
}
