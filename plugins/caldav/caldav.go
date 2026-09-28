package alborzcaldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

var errNoCalendar = alborz.NotFound("notfound.calendar")

func supportsTodo(components []string) bool {
	// Without a component set, a calendar is not promoted to a task list.
	if len(components) == 0 {
		return false
	}
	for _, comp := range components {
		if strings.EqualFold(comp, "VTODO") {
			return true
		}
	}
	return false
}

func supportsEvent(components []string) bool {
	// Servers that advertise no component set are taken to hold events.
	if len(components) == 0 {
		return true
	}
	for _, comp := range components {
		if strings.EqualFold(comp, "VEVENT") {
			return true
		}
	}
	return false
}

type davCollectionProps struct {
	ResourceType struct {
		Calendar *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar,omitempty"`
	} `xml:"resourcetype"`
	DisplayName   string `xml:"displayname"`
	CalendarColor string `xml:"http://apple.com/ns/ical/ calendar-color"`
	ComponentSet  struct {
		Comps []struct {
			Name string `xml:"name,attr"`
		} `xml:"urn:ietf:params:xml:ns:caldav comp"`
	} `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-component-set"`
	PrivilegeSet dav.Privileges `xml:"current-user-privilege-set>privilege"`
}

func (p davCollectionProps) Collection() (name, color string, ok bool) {
	return p.DisplayName, p.CalendarColor, p.ResourceType.Calendar != nil
}

func (p davCollectionProps) Privileges() dav.Privileges { return p.PrivilegeSet }

var calendarColor = dav.Prop{XMLNS: "http://apple.com/ns/ical/", Name: "calendar-color"}

// go-webdav's client has no MKCALENDAR.
func doMkcalendar(ctx context.Context, client *http.Client, path, name, color string, components []string) error {
	var props bytes.Buffer
	fmt.Fprintf(&props, "<D:displayname>%s</D:displayname>", dav.XMLEscape(name))
	props.WriteString(`<C:supported-calendar-component-set>`)
	for _, c := range components {
		fmt.Fprintf(&props, `<C:comp name="%s"/>`, dav.XMLEscape(c))
	}
	props.WriteString(`</C:supported-calendar-component-set>`)
	if color != "" {
		fmt.Fprintf(&props, "<A:calendar-color>%s</A:calendar-color>", dav.XMLEscape(color))
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	fmt.Fprintf(&buf, `<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/"><D:set><D:prop>%s</D:prop></D:set></C:mkcalendar>`, props.String())

	return dav.MakeCollection(ctx, client, "MKCALENDAR", path, buf.Bytes())
}

func listCalendars(ctx context.Context, client *http.Client, baseURL *url.URL, homes []dav.Home) ([]dav.Collection, []error) {
	listed, failed := dav.ListCollections[davCollectionProps](ctx, client, baseURL, homes, `<D:propfind xmlns:D="DAV:" xmlns:A="http://apple.com/ns/ical/" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:resourcetype/><D:displayname/><C:supported-calendar-component-set/><D:current-user-privilege-set/><A:calendar-color/></D:prop></D:propfind>`)
	infos := make([]dav.Collection, len(listed))
	for i, l := range listed {
		infos[i] = l.Collection
		for _, comp := range l.Props.ComponentSet.Comps {
			infos[i].Components = append(infos[i].Components, comp.Name)
		}
	}
	return infos, failed
}

func newClient(u *url.URL, httpClient *http.Client) (*caldav.Client, error) {
	c, err := caldav.NewClient(httpClient, u.String())
	if err != nil {
		return nil, fmt.Errorf("failed to create CalDAV client: %v", err)
	}

	return c, nil
}

func findHome(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	c, err := caldav.NewClient(client, endpoint)
	if err != nil {
		return "", err
	}
	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to query CalDAV principal: %w", err)
	}
	homeSet, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return "", fmt.Errorf("failed to query CalDAV calendar home set: %w", err)
	}
	return homeSet, nil
}

func (p *plugin) clientWithCalendars(ctx context.Context, login *alborz.Login) (*caldav.Client, []dav.Collection, error) {
	return dav.Opened(ctx, p.dav, login, p.client)
}

type CalendarObject struct {
	*caldav.CalendarObject

	Account string
	// Color is a subscribed feed's own, since it has no calendar to take one from.
	Color string
	// ReadOnly marks a subscribed feed's events: no edit, no delete.
	ReadOnly bool
}

// Alarm is a reminder an object carries; alborz shows it but never fires it.
type Alarm struct {
	At     time.Time
	Action string
}

// Alarms resolves the object's triggers to the moments they fire.
func (ao CalendarObject) Alarms() []Alarm {
	var comp *ical.Component
	for _, child := range ao.Data.Children {
		if child.Name == ical.CompEvent || child.Name == ical.CompToDo {
			comp = child
			break
		}
	}
	if comp == nil {
		return nil
	}
	start, _ := comp.Props.DateTime(ical.PropDateTimeStart, nil)
	end := componentEnd(comp)

	var out []Alarm
	for _, child := range comp.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		prop := child.Props.Get(ical.PropTrigger)
		if prop == nil {
			continue
		}
		action, _ := child.Props.Text(ical.PropAction)
		var at time.Time
		if prop.ValueType() == ical.ValueDateTime {
			at, _ = prop.DateTime(nil)
		} else if offset, err := prop.Duration(); err == nil {
			base := start
			if prop.Params.Get(ical.ParamRelated) == "END" && !end.IsZero() {
				base = end
			}
			if !base.IsZero() {
				at = base.Add(offset)
			}
		}
		if at.IsZero() {
			continue
		}
		out = append(out, Alarm{At: at, Action: action})
	}
	return out
}

func componentEnd(comp *ical.Component) time.Time {
	switch comp.Name {
	case ical.CompEvent:
		event := ical.Event{Component: comp}
		end, _ := event.DateTimeEnd(nil)
		return end
	case ical.CompToDo:
		if due, _ := comp.Props.DateTime(ical.PropDue, nil); !due.IsZero() {
			return due
		}
		start, _ := comp.Props.DateTime(ical.PropDateTimeStart, nil)
		if start.IsZero() {
			return time.Time{}
		}
		if durProp := comp.Props.Get(ical.PropDuration); durProp != nil {
			if d, err := durProp.Duration(); err == nil {
				return start.Add(d)
			}
		}
		return start
	}
	return time.Time{}
}

// Occurrence is one instance of an event: a component and the moment it falls on.
type Occurrence struct {
	CalendarObject
	Event *ical.Event
	Start time.Time
	End   time.Time
}

// AllDay reports whether this occurrence occupies whole days.
func (o Occurrence) AllDay() bool {
	prop := o.Event.Props.Get(ical.PropDateTimeStart)
	return prop != nil && prop.ValueType() == ical.ValueDate
}

// UID names the event within its object.
func (o Occurrence) UID() string {
	uid, _ := o.Event.Props.Text(ical.PropUID)
	return uid
}

// Summary is what the row says.
func (o Occurrence) Summary() string {
	summary, _ := o.Event.Props.Text(ical.PropSummary)
	return summary
}

// Star is the colour the occurrence's own event is marked in, or empty.
func (o Occurrence) Star() string {
	return componentColor(o.Event.Component)
}

func componentColor(comp *ical.Component) string {
	name, _ := comp.Props.Text(ical.PropColor)
	if !slices.Contains(alborzbase.FlagColors[:], name) {
		return ""
	}
	return name
}

func setComponentColor(comp *ical.Component, name string) {
	if name == "" {
		comp.Props.Del(ical.PropColor)
		return
	}
	comp.Props.SetText(ical.PropColor, name)
}

func validStarView(view string) bool {
	return view == "" || view == alborzbase.ViewStarred || slices.Contains(alborzbase.FlagColors[:], view)
}

func occurrences(obj CalendarObject, loc *time.Location, start, end time.Time) []Occurrence {
	events := obj.Data.Events()

	// A RECURRENCE-ID override takes the slot its own series still produces for it (RFC 5545 3.8.4.4).
	type slot struct {
		uid string
		at  int64
	}
	overridden := make(map[slot]bool)
	for i := range events {
		if prop := events[i].Props.Get(ical.PropRecurrenceID); prop != nil {
			if at, err := prop.DateTime(loc); err == nil {
				uid, _ := events[i].Props.Text(ical.PropUID)
				overridden[slot{uid, at.Unix()}] = true
			}
		}
	}

	var out []Occurrence
	for i := range events {
		event := &events[i]
		first, _ := event.DateTimeStart(nil)
		last, _ := event.DateTimeEnd(nil)
		span := last.Sub(first)
		if span < 0 {
			span = 0
		}

		// A rule still on the component was not expanded by the server.
		var instances []time.Time
		if prop := event.Props.Get(ical.PropRecurrenceRule); prop != nil && strings.Contains(strings.ToUpper(prop.Value), "RSCALE=") {
			dtstart, _ := event.Props.DateTime(ical.PropDateTimeStart, loc)
			instances = expandRule(prop.Value, dtstart, start.Add(-span), end)
		} else if set, err := event.RecurrenceSet(loc); err == nil && set != nil {
			instances = set.Between(start.Add(-span), end, true)
		} else {
			out = append(out, Occurrence{CalendarObject: obj, Event: event, Start: first, End: last})
			continue
		}
		uid, _ := event.Props.Text(ical.PropUID)
		for _, at := range instances {
			if overridden[slot{uid, at.Unix()}] {
				continue
			}
			out = append(out, Occurrence{
				CalendarObject: obj, Event: event,
				Start: at, End: at.Add(span),
			})
		}
	}
	return out
}

func (ao CalendarObject) URL() string {
	return "/calendar/" + url.PathEscape(ao.Path)
}

// AllDay reports whether the object's start is a bare date.
func (ao CalendarObject) AllDay() bool {
	events := ao.Data.Events()
	if len(events) == 0 {
		return false
	}
	prop := events[0].Props.Get(ical.PropDateTimeStart)
	return prop != nil && prop.ValueType() == ical.ValueDate
}

type TaskObject struct {
	*caldav.CalendarObject

	Account string
}

func (t TaskObject) URL() string {
	return "/tasks/" + url.PathEscape(t.Path)
}

func (p *plugin) pooledCalendars(ctx *alborz.Context) ([]dav.Account[*caldav.Client], error) {
	return dav.Pooled(ctx, p.dav, p.clientWithCalendars, errNoCalendar)
}

func querySites(ctx *alborz.Context, sites []dav.Site[*caldav.Client], query *caldav.CalendarQuery) ([]CalendarObject, error) {
	var events []CalendarObject
	for _, r := range dav.Each(ctx.Request().Context(), sites, func(ctx context.Context, site dav.Site[*caldav.Client]) ([]caldav.CalendarObject, error) {
		return site.Client.QueryCalendar(ctx, site.Collection.Path, query)
	}) {
		if r.Err != nil {
			return nil, fmt.Errorf("failed to query calendar %s: %v", r.Site.Collection.Name, r.Err)
		}
		for i := range r.Value {
			events = append(events, CalendarObject{CalendarObject: &r.Value[i], Account: r.Site.Collection.Account})
		}
	}
	return events, nil
}

func (p *plugin) writableGroups(ctx *alborz.Context, supports func([]string) bool) ([]dav.Group, error) {
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return nil, err
	}
	return dav.WritableGroups(accounts, func(cal dav.Collection) bool { return cal.Writable && supports(cal.Components) }), nil
}

func (p *plugin) destination(ctx *alborz.Context, value string, supports func([]string) bool) (dav.Ref[*caldav.Client], error) {
	return dav.Destination(ctx, value, p.clientWithCalendars, func(cal dav.Collection) bool {
		return cal.Writable && supports(cal.Components)
	})
}
