package alborzcaldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/labstack/echo/v4"
)

// The account's domain has no CalDAV server, or it holds no calendar;
// the HTTP layer answers 404 rather than crashing on direct URLs.
var errNoCalendar = alborz.NotFoundf("caldav: no calendar found")

type CalendarInfo struct {
	dav.Collection
	Visible bool
	// Only marks the collection a URL is currently narrowed to, and only
	// when it is the sole one: pressing the same link again is how a
	// reader gets back out of a view they pressed their way into.
	Only                  bool
	SupportedComponentSet []string

	// Account owning the calendar, set only in the unified view
	Account string
}

func (c CalendarInfo) SupportsTodo() bool {
	// VTODO is optional in CalDAV. When a server omits the component-set
	// property, do not promote an ordinary calendar into the Tasks UI.
	if len(c.SupportedComponentSet) == 0 {
		return false
	}
	for _, comp := range c.SupportedComponentSet {
		if strings.EqualFold(comp, "VTODO") {
			return true
		}
	}
	return false
}

func (c CalendarInfo) SupportsEvent() bool {
	// VEVENT is the conservative fallback for older CalDAV servers that
	// do not advertise supported-calendar-component-set.
	if len(c.SupportedComponentSet) == 0 {
		return true
	}
	for _, comp := range c.SupportedComponentSet {
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
func doMkcalendar(ctx context.Context, client *http.Client, path, name string, components []string, color string) error {
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
func listCalendars(ctx context.Context, client *http.Client, baseURL *url.URL, homeSet string) ([]CalendarInfo, error) {
	listed, err := dav.ListCollections[davCollectionProps](ctx, client, baseURL, homeSet, `<D:propfind xmlns:D="DAV:" xmlns:A="http://apple.com/ns/ical/" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:resourcetype/><D:displayname/><C:supported-calendar-component-set/><D:current-user-privilege-set/><A:calendar-color/></D:prop></D:propfind>`)
	if err != nil {
		return nil, err
	}
	infos := make([]CalendarInfo, len(listed))
	for i, l := range listed {
		infos[i] = CalendarInfo{Collection: l.Collection}
		for _, comp := range l.Props.ComponentSet.Comps {
			infos[i].SupportedComponentSet = append(infos[i].SupportedComponentSet, comp.Name)
		}
	}
	return infos, nil
}

func newClient(u *url.URL, httpClient *http.Client) (*caldav.Client, error) {
	c, err := caldav.NewClient(httpClient, u.String())
	if err != nil {
		return nil, fmt.Errorf("failed to create CalDAV client: %v", err)
	}

	return c, nil
}

// createCalendar adds a collection to the account's calendar home and
// forgets the cached list, so the new one appears at once.
func (p *plugin) createCalendar(ctx context.Context, session *alborz.Session, name string, components []string, color string) error {
	c, err := p.client(session)
	if err != nil {
		return err
	}
	davBase, _ := p.dav.URL(session)

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return fmt.Errorf("failed to query CalDAV principal: %v", err)
	}
	homeSet, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return fmt.Errorf("failed to query CalDAV calendar home set: %v", err)
	}

	client := p.dav.HTTPClient(session)
	err = dav.CreateCollection(ctx, davBase, homeSet, name, "calendar", func(ctx context.Context, target string) error {
		return doMkcalendar(ctx, client, target, name, components, color)
	})
	if err != nil {
		return err
	}
	p.calendars.Forget(session.Username())
	return nil
}

func (p *plugin) clientWithCalendars(ctx context.Context, session *alborz.Session) (*caldav.Client, []CalendarInfo, error) {
	c, err := p.client(session)
	if err != nil {
		return nil, nil, err
	}
	davBase, _ := p.dav.URL(session)

	// Principal, home set, and calendar list are three sequential round
	// trips answering which calendars the account has, so they are found
	// once per user rather than on every page. The load outlives the
	// request that starts it: a second page waiting on it must not be
	// failed by the first one's reader going away.
	infos, err := p.calendars.Get(session.Username(), func() ([]CalendarInfo, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryTimeout)
		defer cancel()

		principal, err := c.FindCurrentUserPrincipal(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to query CalDAV principal: %v", err)
		}

		calendarHomeSet, err := c.FindCalendarHomeSet(ctx, principal)
		if err != nil {
			return nil, fmt.Errorf("failed to query CalDAV calendar home set: %v", err)
		}

		infos, err := listCalendars(ctx, p.dav.HTTPClient(session), davBase, calendarHomeSet)
		if err != nil {
			return nil, fmt.Errorf("failed to find calendars: %v", err)
		}
		return infos, nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(infos) == 0 {
		return nil, nil, errNoCalendar
	}

	return c, infos, nil
}

type CalendarObject struct {
	*caldav.CalendarObject

	// Account owning the object, set only in the unified view
	Account string
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

// Summary is what the row says.
func (o Occurrence) Summary() string {
	summary, _ := o.Event.Props.Text(ical.PropSummary)
	return summary
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
		// it, so it is expanded here over the window being drawn.
		set, err := event.RecurrenceSet(loc)
		if err != nil || set == nil {
			out = append(out, Occurrence{CalendarObject: obj, Event: event, Start: first, End: last})
			continue
		}
		for _, at := range set.Between(start.Add(-span), end, true) {
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
func (p *plugin) pooledCalendars(ctx *alborz.Context) ([]dav.Account[*caldav.Client, CalendarInfo], error) {
	return dav.Pooled(ctx, p.dav, p.clientWithCalendars,
		func(cal *CalendarInfo, account string) { cal.Account = account }, errNoCalendar)
}

// querySite is one visible calendar to query, bound to its account's client.
type querySite struct {
	cal      CalendarInfo
	client   *caldav.Client
	settings *Settings
}

// querySites runs the calendar query against every site and tags each
// result with its owning account.
func querySites(ctx *alborz.Context, sites []querySite, query *caldav.CalendarQuery) ([]CalendarObject, error) {
	var events []CalendarObject
	for _, r := range dav.Each(ctx.Request().Context(), sites, func(ctx context.Context, site querySite) ([]caldav.CalendarObject, error) {
		return site.client.QueryCalendar(ctx, site.cal.Path, query)
	}) {
		if r.Err != nil {
			return nil, fmt.Errorf("failed to query calendar %s: %v", r.Site.cal.Name, r.Err)
		}
		for i := range r.Value {
			events = append(events, CalendarObject{CalendarObject: &r.Value[i], Account: r.Site.cal.Account})
		}
	}
	return events, nil
}

func (p *plugin) writableGroups(ctx *alborz.Context, supports func(CalendarInfo) bool) ([]dav.Group[CalendarInfo], error) {
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return nil, err
	}
	return dav.WritableGroups(accounts, func(cal CalendarInfo) bool { return cal.Writable && supports(cal) }), nil
}

// resolveCreateCalendar turns a create form's "account|path" choice into
// that account's client and the bare path, refusing unknown targets.
func (p *plugin) resolveCreateCalendar(ctx *alborz.Context, value string, supports func(CalendarInfo) bool) (*caldav.Client, string, string, error) {
	acct, calPath, ok := strings.Cut(value, "|")
	session := ctx.SessionFor(acct)
	if !ok || session == nil {
		return nil, "", "", echo.NewHTTPError(http.StatusBadRequest, "unknown calendar")
	}
	c, cals, err := p.clientWithCalendars(ctx.Request().Context(), session)
	if err != nil {
		return nil, "", "", err
	}
	for _, cal := range cals {
		if cal.Writable && supports(cal) && cal.Path == calPath {
			return c, calPath, acct, nil
		}
	}
	return nil, "", "", echo.NewHTTPError(http.StatusBadRequest, "unknown calendar")
}
