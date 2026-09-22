package alborzcaldav

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

// eventListParams are what decide which events a view holds: an event's
// page is opened with them and returns to them.
var eventListParams = []string{"account", "cal", "month", "date", "view", "span"}

type EventRenderData struct {
	alborz.BaseRenderData
	Rail     dav.Rail
	Calendar *dav.Collection
	Event    CalendarObject
	// Authors are who added the object and changed it last, where it is
	// kept here and that is another account.
	Authors dav.Authors
	// List is the view the page was opened from; see TaskRenderData.
	List       string
	Star       string
	Neighbours dav.Neighbours
	// Repeats says the event's rule in words, or nothing, and
	// RepeatsUntil its last day, kept apart to be read left to right.
	Repeats, RepeatsUntil string
}

type UpdateEventRenderData struct {
	alborz.BaseRenderData
	Rail           dav.Rail
	Groups         []dav.Group
	Calendar       *dav.Collection
	CalendarObject *caldav.CalendarObject // nil if creating a new event
	Event          *ical.Event

	// The two shapes the form offers, filled whichever one applies so
	// that ticking the box does not lose what was already typed.
	AllDay    bool
	StartTime string // datetime-local
	EndTime   string
	StartDate string // date, and the last day rather than the day after
	EndDate   string
	// Attendees are the addresses invited, one per line. Naming anybody
	// makes this account the organizer and sends an invitation on save.
	Attendees string
	Repeat    Repeat
	// RepeatFreqs are the rules the form offers, in its order.
	RepeatFreqs []string
}

// newEventStart reads the ?date= a create link carries and keeps the
// current time of day on it, so a form opened from a day lands on that
// day rather than on today.
func newEventStart(ctx *alborz.Context, loc *time.Location) time.Time {
	now := time.Now().In(loc)
	d, err := time.ParseInLocation(datePageLayout, ctx.QueryParam("date"), loc)
	if err != nil {
		return now
	}
	return time.Date(d.Year(), d.Month(), d.Day(), now.Hour(), now.Minute(), 0, 0, loc)
}

// fillEventForm gives the form both shapes of the same event: the times
// it has, and the days it covers. Ticking the box then costs nothing
// that was already entered. The last day is shown, not the day after -
// DTEND is exclusive in iCalendar and inclusive in every head.
//
// when is the day a new event starts on: the page that opened the form
// knows which day the reader is looking at, and typing it again is the
// only alternative. It is ignored once the event has a start of its own.
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

// dayQuery and monthQuery are what an event's page is opened with: the
// list it came from, named in full. The month and the day both have a
// default the URL leaves out, and an event page cannot rebuild a list
// it was not told about.
func dayQuery(ctx *alborz.Context, start time.Time) string {
	q := dav.ListParams(ctx, "account", "cal")
	q.Set("date", start.Format(datePageLayout))
	return alborz.Query(q)
}

func monthQuery(ctx *alborz.Context, mv monthView) string {
	q := dav.ListParams(ctx, "account", "cal", "span", "group")
	q.Set("month", mv.page)
	if mv.view != "" {
		q.Set("view", mv.view)
	}
	return alborz.Query(q)
}

// eventKey names one row of an event list. An object's path is enough
// for a calendar, where a recurring event is one object however many
// times it is drawn; a subscribed feed is a single object holding every
// event, so there the UID within it is what tells them apart.
func eventKey(path, uid string, feed bool) string {
	if feed {
		return path + "#" + uid
	}
	return path
}

// eventItems are the events of the list the reader came from - the day
// where the URL names one, the agenda where it names that - in the
// order that list showed them. An event reached without a list has no
// neighbours, and says so by having none.
func (p *plugin) eventItems(ctx *alborz.Context) ([]dav.Item, error) {
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

	params := dav.RowParams(ctx, "", dav.ListParams(ctx, eventListParams...))
	var items []dav.Item
	seen := map[string]bool{}
	for _, oc := range shown {
		uid, _ := oc.Event.Props.Text("UID")
		key := eventKey(oc.Path, uid, oc.ReadOnly)
		if seen[key] {
			continue
		}
		seen[key] = true
		href := dav.ObjectURL("/calendar/", oc.Path, oc.Account, params)
		if oc.ReadOnly {
			href = dav.ObjectURL("/calendar/", oc.Path, oc.Account, withUID(params, uid))
		}
		items = append(items, dav.Item{Path: key, URL: href})
	}
	return items, nil
}

// withUID names the event within a subscribed feed, which is one object
// holding all of them.
func withUID(params url.Values, uid string) url.Values {
	q := url.Values{"uid": {uid}}
	for key, values := range params {
		q[key] = values
	}
	return q
}

func (p *plugin) event(ctx *alborz.Context) error {
	path, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	c, calendars, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
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
			Comps: []caldav.CalendarCompRequest{{
				Name: "VEVENT",
				Props: []string{
					"SUMMARY",
					"DESCRIPTION",
					"UID",
					"DTSTART",
					"DTEND",
					"DURATION",
					"COLOR",
				},
			}},
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
		Neighbours:     dav.Around(items, eventKey(path, "", false)),
		Authors:        p.dav.Authors(event.Path, ctx.Session.Username()),
	}
	data.Repeats, data.RepeatsUntil = repeatWords(ctx, &vevents[0], alborzbase.UserLocation(ctx))
	return ctx.Render(http.StatusOK, "event.html", data)
}

// feedEvent shows one event of a subscribed feed. The feed is one
// object holding every event, so the URL names the event by UID and
// the page is given a calendar holding that event alone, with the
// zones it may refer to. Nothing on it can be edited.
func (p *plugin) feedEvent(ctx *alborz.Context, address string) error {
	info, cal, err := feedObject(ctx, address, ctx.QueryParam("uid"))
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
		Neighbours:     dav.Around(items, eventKey(address, ctx.QueryParam("uid"), true)),
	}
	data.Repeats, data.RepeatsUntil = repeatWords(ctx, &event, alborzbase.UserLocation(ctx))
	return ctx.Render(http.StatusOK, "event.html", data)
}

// feedObject is one event of a subscribed feed as a calendar of its
// own, with the zones it may refer to. A subscription is the account's
// own, kept in its settings beside the server's calendars rather than
// among them.
func feedObject(ctx *alborz.Context, address, uid string) (*dav.Collection, *ical.Calendar, error) {
	settings, err := loadSettings(ctx.Session.Store())
	if err != nil {
		return nil, nil, err
	}
	var info *dav.Collection
	for _, s := range settings.Subscriptions {
		if s.URL == address {
			i := s.info(ctx.Session.Username())
			info = &i
		}
	}
	if info == nil {
		return nil, nil, alborz.NotFound("notfound.subscription", address)
	}
	feed, ok := subs.events(address, info.Color)
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
	calendarObjectPath, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	loc := alborzbase.UserLocation(ctx)

	var c *caldav.Client
	var calendars []dav.Collection
	var groups []dav.Group
	var co *caldav.CalendarObject
	var event *ical.Event
	var currentCalendar *dav.Collection
	if calendarObjectPath != "" {
		c, calendars, err = p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
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
		// A recurring event with rewritten instances holds several
		// VEVENTs; the one without RECURRENCE-ID is the series, and
		// the form edits that.
		events := co.Data.Events()
		for i := range events {
			if events[i].Props.Get(ical.PropRecurrenceID) == nil {
				event = &events[i]
			}
		}
		if event == nil {
			return fmt.Errorf("calendar object %q holds no event to edit", calendarObjectPath)
		}
		currentCalendar = dav.Holding(calendars, "", co.Path)
	} else {
		// Creating is pooled: it must not fail merely because the active
		// account has no CalDAV collection of this kind.
		groups, err = p.writableGroups(ctx, supportsEvent)
		if err != nil {
			return err
		}
		if len(groups) == 0 || len(groups[0].Collections) == 0 {
			return alborz.RenderInfo(ctx, http.StatusOK, ctx.T("calendar.nowritable"))
		}
		event = ical.NewEvent()
		event.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
		currentCalendar = &groups[0].Collections[0]
	}

	if ctx.Request().Method == "POST" {
		summary := ctx.FormValue("summary")
		description := ctx.FormValue("description")

		// The form answers its own invalid input: the same page with
		// an alert, never a status page the browser writes.
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

		to := dav.Ref[*caldav.Client]{Client: c}
		creating := co == nil
		if creating {
			to, err = p.destination(ctx, ctx.FormValue("calendar"), supportsEvent)
			if errors.Is(err, dav.ErrNoDestination) {
				return reject(ctx.T("form.destinationneeded"))
			} else if err != nil {
				return err
			}
		}

		// An event that occupies whole days has no time of day to
		// ask for, and asking anyway is what made a two-day
		// conference impossible to enter.
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
			// The form asks for the last day; iCalendar 3.8.2.2
			// wants the day after it, since DTEND is exclusive.
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

		// Naming anybody makes this an invitation: the account
		// becomes the organizer and everyone listed is asked
		// (RFC 5546). Removing the last one withdraws it.
		attendees := parseAttendees(ctx.FormValue("attendees"))
		had := attendeeLines(event) != ""
		setScheduling(event, ctx.Session.Username(), attendees)

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

		// The event is saved before anybody is told about it: a send
		// that fails must not lose what was written, so the failure
		// is reported and the meeting stays.
		method := alborzbase.MethodRequest
		told := attendees
		if len(attendees) == 0 && had {
			method, told = alborzbase.MethodCancel, parseAttendees(ctx.FormValue("attendees_was"))
		}
		if err := sendScheduling(ctx, event, told, method); err != nil {
			ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.T("invite.sendfailed")})
			ctx.Logger().Printf("failed to send the scheduling message: %v", err)
		} else if len(told) > 0 {
			ctx.PutNotice(ctx.T("invite.sent"))
		}

		return dav.Saved(ctx, creating, ctx.T("notice.eventcreated"), summary, CalendarObject{CalendarObject: co}.URL(), "/calendar", to.Account)
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

// eventList is the view an event's page came from: the list its row
// named, or else the day when the request names one, the month
// otherwise.
func eventList(ctx *alborz.Context) string {
	if from := ctx.From(); from != "" {
		return from
	}
	params := dav.ListParams(ctx, eventListParams...)
	if params.Get("date") != "" {
		return dav.ListURL("/calendar/date", params)
	}
	return dav.ListURL("/calendar", params)
}
