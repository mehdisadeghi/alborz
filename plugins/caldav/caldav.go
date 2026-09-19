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

// The account's domain has no CalDAV server, or it holds no calendar;
// the HTTP layer answers 404 rather than crashing on direct URLs.
var errNoCalendar = alborz.NotFound("notfound.calendar")

func supportsTodo(components []string) bool {
	// VTODO is optional in CalDAV. When a server omits the component-set
	// property, do not promote an ordinary calendar into the Tasks UI.
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
	// VEVENT is the conservative fallback for older CalDAV servers that
	// do not advertise supported-calendar-component-set.
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

// calendarColor is Apple's colour property, which every server that
// shows colours reads.
var calendarColor = dav.Prop{XMLNS: "http://apple.com/ns/ical/", Name: "calendar-color"}

// doMkcalendar creates a calendar collection. go-webdav's client speaks
// only to existing collections, so the request is built here: a name, the
// components the collection accepts, and Apple's colour property, which
// every server that shows colours reads.
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

// listCalendars fetches the calendar list with names, colors, and supported
// component sets in a single PROPFIND.
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

// findHome is where a source lists the account's calendars.
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

func (p *plugin) clientWithCalendars(ctx context.Context, session *alborz.Session) (*caldav.Client, []dav.Collection, error) {
	return dav.Opened(ctx, p.dav, session, p.client)
}

type CalendarObject struct {
	*caldav.CalendarObject

	// Account owning the object, set only in the unified view
	Account string
	// Color is set for a subscribed feed, which has no calendar in the
	// account to take a colour from; the views prefer it over the
	// per-calendar colour map.
	Color string
	// ReadOnly marks a subscribed feed's events: no edit, no delete.
	ReadOnly bool
}

// Alarm is a reminder an object carries (RFC 5545 3.6.6). Nothing here
// acts on one - Alborz is request-driven and has nothing that wakes up -
// but an alarm set in another client is part of the event, and showing
// it is the difference between "no reminder" and "a reminder this page
// will not tell you about".
type Alarm struct {
	At time.Time
	// Action is what the alarm asks for: DISPLAY, EMAIL, AUDIO. Apple
	// writes NONE for an event it deliberately leaves silent.
	Action string
}

// Alarms are the moments the object asks to be reminded at, resolved
// against the component they hang on: a trigger is usually an offset
// from the start, sometimes from the end, and occasionally an instant.
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
	end, _ := comp.Props.DateTime(ical.PropDateTimeEnd, nil)

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

// Occurrence is one drawing of an event: a component of an object plus
// the moment it falls on. A recurring event has many and the component
// names only the first, so a day holds occurrences rather than objects.
//
// Two shapes arrive. A server honouring the expand request returns one
// component per instance (RFC 4791 9.6.5); one that ignores it returns
// the master carrying its rule. Both are drawn, which is what lets a
// recurring event made in another client appear here.
type Occurrence struct {
	CalendarObject
	Event *ical.Event
	Start time.Time
	End   time.Time
}

// AllDay reports whether this occurrence occupies whole days, read from
// its own component rather than the object's first.
func (o Occurrence) AllDay() bool {
	prop := o.Event.Props.Get(ical.PropDateTimeStart)
	return prop != nil && prop.ValueType() == ical.ValueDate
}

// UID names the event within its object, which is what a feed's page
// needs: a subscribed feed is one object holding every event.
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

// componentColor reads COLOR (RFC 7986 5.9) as one of the seven names;
// any other value is another client's and shows as no star.
func componentColor(comp *ical.Component) string {
	name, _ := comp.Props.Text(ical.PropColor)
	if !slices.Contains(alborzbase.FlagColors[:], name) {
		return ""
	}
	return name
}

// setComponentColor writes one of the seven, or clears it.
func setComponentColor(comp *ical.Component, name string) {
	if name == "" {
		comp.Props.Del(ical.PropColor)
		return
	}
	comp.Props.SetText(ical.PropColor, name)
}

// validStarView is a view a list of marked objects answers: nothing,
// starred, or one of the seven.
func validStarView(view string) bool {
	return view == "" || view == alborzbase.ViewStarred || slices.Contains(alborzbase.FlagColors[:], view)
}

// occurrences lists every instance of an object that begins before end
// and ends after start, in the display timezone.
func occurrences(obj CalendarObject, loc *time.Location, start, end time.Time) []Occurrence {
	events := obj.Data.Events()

	// An instance rewritten on its own - moved, renamed - names in
	// RECURRENCE-ID the start it stands in for. The rule still produces
	// that start, so the slot is left to the override, which is listed
	// as the plain event it is.
	overridden := make(map[int64]bool)
	for i := range events {
		if prop := events[i].Props.Get(ical.PropRecurrenceID); prop != nil {
			if at, err := prop.DateTime(loc); err == nil {
				overridden[at.Unix()] = true
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

		// A rule still on the component means the server did not expand
		// it, so it is expanded here over the window being drawn. One
		// that counts in another calendar (RFC 7529) is beyond the rule
		// library and expanded by hand.
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
		for _, at := range instances {
			if overridden[at.Unix()] {
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

// AllDay reports a start given as a bare date, iCalendar's way of saying
// the event occupies whole days instead of a span of clock time. Such an
// event has no start time to show, and a formatted one would be the
// timezone's midnight, not a fact about the event.
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

	// Account owning the object, set only in the unified view
	Account string
}

func (t TaskObject) URL() string {
	return "/tasks/" + url.PathEscape(t.Path)
}

// pooledCalendars resolves every signed-in account that has CalDAV:
// calendar pages are always pooled across accounts.
func (p *plugin) pooledCalendars(ctx *alborz.Context) ([]dav.Account[*caldav.Client], error) {
	return dav.Pooled(ctx, p.dav, p.clientWithCalendars, errNoCalendar)
}

// querySites runs the calendar query against every site and tags each
// result with its owning account.
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

// destination is the calendar a create form chose, when it is one the
// account can write the kind of object being made into.
func (p *plugin) destination(ctx *alborz.Context, value string, supports func([]string) bool) (dav.Ref[*caldav.Client], error) {
	return dav.Destination(ctx, value, p.clientWithCalendars, func(cal dav.Collection) bool {
		return cal.Writable && supports(cal.Components)
	})
}
