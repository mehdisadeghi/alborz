package calendar

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/web"
	"github.com/alborzmail/go-recur"
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

func makeCalendar(ctx context.Context, client *http.Client, base *url.URL, path, name, color string, components []string) error {
	c, err := caldav.NewClient(client, base.String())
	if err != nil {
		return err
	}
	return c.CreateCalendar(ctx, &caldav.Calendar{Path: path, Name: name, Color: color, SupportedComponentSet: components})
}

func changeCalendar(ctx context.Context, client *http.Client, base *url.URL, path, name, color string) error {
	c, err := caldav.NewClient(client, base.String())
	if err != nil {
		return err
	}
	update := &caldav.CalendarUpdate{Name: &name}
	if color != "" {
		update.Color = &color
	}
	return c.UpdateCalendar(ctx, path, update)
}

func listCalendars(ctx context.Context, client *http.Client, baseURL *url.URL, homes []dav.Home) ([]dav.Collection, []error) {
	c, err := caldav.NewClient(client, baseURL.String())
	if err != nil {
		return nil, []error{err}
	}
	return dav.ListCollections(ctx, homes, func(ctx context.Context, home string) ([]dav.Collection, error) {
		cals, err := c.FindCalendars(ctx, home)
		if err != nil {
			return nil, err
		}
		out := make([]dav.Collection, len(cals))
		for i, cal := range cals {
			out[i] = dav.Collection{Path: cal.Path, Name: cal.Name, Color: cal.Color, Writable: !cal.ReadOnly, Components: cal.SupportedComponentSet, RScales: cal.SupportedRScaleSet}
		}
		return out, nil
	})
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

// Open is the login's client with its calendars.
func (p *Calendars) Open(ctx context.Context, login *alborz.Login) (*caldav.Client, []dav.Collection, error) {
	return dav.Opened(ctx, p.DAV, login, p.Client)
}

type CalendarObject struct {
	*caldav.CalendarObject

	Account string
	// ReadOnly marks an object whose calendar this account may not write: no edit, no delete.
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

	var out []Alarm
	for _, child := range comp.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		// An alarm with no time to place it on is not shown.
		at, err := comp.TriggerTime(child, nil)
		if err != nil {
			continue
		}
		action, _ := child.Props.Text(ical.PropAction)
		out = append(out, Alarm{At: at, Action: action})
	}
	return out
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
	if !slices.Contains(alborz.FlagColors[:], name) {
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
	return view == "" || view == web.ViewStarred || slices.Contains(alborz.FlagColors[:], view)
}

func occurrences(obj CalendarObject, loc *time.Location, start, end time.Time) []Occurrence {
	var out []Occurrence
	seen := make(map[string]bool)
	for _, event := range obj.Data.Events() {
		uid, _ := event.Props.Text(ical.PropUID)
		if seen[uid] {
			continue
		}
		seen[uid] = true
		series, err := obj.Data.Series(uid, loc)
		var instances iter.Seq[recur.Instance]
		if err == nil {
			instances, err = series.Between(start, end)
		}
		if err != nil {
			// A series not read here shows each component once, at its own start: a wrong instance is worse than a missing one.
			for _, event := range obj.Data.Events() {
				if id, _ := event.Props.Text(ical.PropUID); id == uid {
					first, _ := event.DateTimeStart(loc)
					last, _ := event.DateTimeEnd(loc)
					out = append(out, Occurrence{CalendarObject: obj, Event: &event, Start: first, End: last})
				}
			}
			continue
		}
		for inst := range instances {
			out = append(out, Occurrence{
				CalendarObject: obj, Event: &ical.Event{Component: series.Component(inst)},
				Start: inst.Start, End: inst.End,
			})
		}
	}
	return out
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

// OpenWithFeeds is Open, the calendars of subscriptions another alborz added to the settings made here first.
func (p *Calendars) OpenWithFeeds(ctx context.Context, login *alborz.Login) (*caldav.Client, []dav.Collection, error) {
	err := p.follows(ctx, login, func(c store.Collection) error {
		// A feed that is down leaves its calendar empty until the next round, not the page failing.
		if err := p.feeds.refresh(ctx, c.Feed.URL); err != nil {
			p.feeds.log.Printf("calendar: feed %s: %v", c.Feed.URL, err)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return p.Open(ctx, login)
}

// pooledCalendars lists every account's calendars.
func (p *plugin) pooledCalendars(ctx *web.Context) ([]pim.Account[*caldav.Client], error) {
	return pim.Pooled(ctx, p.DAV, p.OpenWithFeeds, errNoCalendar)
}

// QuerySites asks each site for the objects query matches.
func (p *Calendars) QuerySites(ctx context.Context, sites []pim.Site[*caldav.Client], query *caldav.CalendarQuery) ([]CalendarObject, error) {
	var events []CalendarObject
	for _, r := range pim.Present(p.DAV, dav.Across(ctx, sites, pim.Site[*caldav.Client].Account, func(ctx context.Context, site pim.Site[*caldav.Client]) ([]caldav.CalendarObject, error) {
		return site.Client.QueryCalendar(ctx, site.Collection.Path, query)
	})) {
		if r.Err != nil {
			return nil, fmt.Errorf("failed to query calendar %s: %v", r.Site.Collection.Name, r.Err)
		}
		for i := range r.Value {
			events = append(events, CalendarObject{CalendarObject: &r.Value[i], Account: r.Site.Collection.Account,
				ReadOnly: !r.Site.Collection.Writable})
		}
	}
	return events, nil
}

func (p *plugin) writableGroups(ctx *web.Context, supports func([]string) bool) ([]dav.Group, error) {
	accounts, err := p.pooledCalendars(ctx)
	if err != nil {
		return nil, err
	}
	return pim.WritableGroups(accounts, func(cal dav.Collection) bool { return cal.Writable && supports(cal.Components) }), nil
}

func (p *plugin) destination(ctx *web.Context, value string, supports func([]string) bool) (pim.Ref[*caldav.Client], error) {
	return pim.Destination(ctx, value, p.Open, func(cal dav.Collection) bool {
		return cal.Writable && supports(cal.Components)
	})
}
