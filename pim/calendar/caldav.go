package calendar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	"github.com/alborzmail/go-recur"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

var ErrNoCalendar = alborz.NotFound("notfound.calendar")

func SupportsTodo(components []string) bool {
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

func SupportsEvent(components []string) bool {
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
	// Calendar is the name of the collection it was read from.
	Calendar string
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
	return ComponentColor(o.Event.Component)
}

func ComponentColor(comp *ical.Component) string {
	name, _ := comp.Props.Text(ical.PropColor)
	if !slices.Contains(alborz.FlagColors[:], name) {
		return ""
	}
	return name
}

func SetComponentColor(comp *ical.Component, name string) {
	if name == "" {
		comp.Props.Del(ical.PropColor)
		return
	}
	comp.Props.SetText(ical.PropColor, name)
}

// Occurrences places obj's events between start and end. The error names each series not read, whose components
// are drawn at their own start or, where that does not read either, left out; it never stops the others.
func Occurrences(obj CalendarObject, loc *time.Location, start, end time.Time) ([]Occurrence, error) {
	var out []Occurrence
	var errs []error
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
			errs = append(errs, fmt.Errorf("%s: %w", obj.Path, err))
			for _, event := range obj.Data.Events() {
				if id, _ := event.Props.Text(ical.PropUID); id == uid {
					first, err := event.DateTimeStart(loc)
					if err != nil {
						continue
					}
					// An end that does not read leaves the event at its start.
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
	return out, errors.Join(errs...)
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

// Unparsed is the object of calendar the client could not parse.
func Unparsed(calendar string, obj *caldav.CalendarObject) pim.Unread {
	_, err := ical.NewDecoder(bytes.NewReader(obj.Raw)).Decode()
	return pim.Unread{Collection: calendar, Err: fmt.Errorf("%s: %w", obj.Path, err)}
}

// QuerySites asks each site for the objects query matches. A calendar that fails and an object that does not
// parse are returned as unread, so that they cost the page nothing else.
func (p *Calendars) QuerySites(ctx context.Context, sites []pim.Site[*caldav.Client], query *caldav.CalendarQuery) ([]CalendarObject, []pim.Unread) {
	var events []CalendarObject
	var unread []pim.Unread
	for _, r := range pim.Present(p.DAV, dav.Across(ctx, sites, pim.Site[*caldav.Client].Account, func(ctx context.Context, site pim.Site[*caldav.Client]) ([]caldav.CalendarObject, error) {
		return site.Client.QueryCalendar(ctx, site.Collection.Path, query)
	})) {
		name := r.Site.Collection.Name
		if r.Err != nil {
			unread = append(unread, pim.Unread{Collection: name, Err: r.Err})
			continue
		}
		for i := range r.Value {
			if r.Value[i].Data == nil {
				unread = append(unread, Unparsed(name, &r.Value[i]))
				continue
			}
			events = append(events, CalendarObject{CalendarObject: &r.Value[i], Account: r.Site.Collection.Account,
				Calendar: name, ReadOnly: !r.Site.Collection.Writable})
		}
	}
	return events, unread
}

func FirstEvent(cal *ical.Calendar) *ical.Component {
	if evs := cal.Events(); len(evs) > 0 {
		return evs[0].Component
	}
	return nil
}

func FirstTodo(cal *ical.Calendar) *ical.Component {
	for _, child := range cal.Children {
		if child.Name == ical.CompToDo {
			return child
		}
	}
	return nil
}

func EventQuery(start, end time.Time) caldav.CalendarQuery {
	return caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:  "VCALENDAR",
			Props: []string{"VERSION"},
			// The whole event: an unexpanded rule must arrive, or only the first instance is drawn.
			Comps: []caldav.CalendarCompRequest{{
				Name: "VEVENT", AllProps: true, AllComps: true,
			}},
			Expand: &caldav.CalendarExpandRequest{Start: start, End: end},
		},
		CompFilter: caldav.CompFilter{
			Name:  "VCALENDAR",
			Comps: []caldav.CompFilter{{Name: "VEVENT", Start: start, End: end}},
		},
	}
}

// GetObject is the object at p, or alborz.ErrUnreadable for an answer that does not parse.
func GetObject(ctx context.Context, c *caldav.Client, p string) (*caldav.CalendarObject, error) {
	co, err := c.GetCalendarObject(ctx, p)
	// What is neither the server's status nor the transport's failure, a *url.Error, is the answer.
	var transport net.Error
	if _, answered := webdav.HTTPErrorCode(err); err != nil && !answered && !errors.As(err, &transport) && ctx.Err() == nil {
		return nil, fmt.Errorf("%w: %s: %v", alborz.ErrUnreadable, p, err)
	}
	return co, err
}

func ChangeComponent(ctx context.Context, ref pim.Ref[*caldav.Client], comp func(*ical.Calendar) *ical.Component, change func(*ical.Component)) (*caldav.CalendarObject, error) {
	co, err := GetObject(ctx, ref.Client, ref.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to get object: %w", err)
	}
	target := comp(co.Data)
	if target == nil {
		return nil, errors.New("the object holds no such component")
	}
	change(target)
	target.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	target.Props.SetDateTime(ical.PropLastModified, time.Now().UTC())
	_, err = ref.Client.PutCalendarObject(ctx, co.Path, co.Data, nil)
	return co, err
}

func NewCalendar(comp *ical.Component) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, ItipProductID)
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Children = append(cal.Children, comp)
	return cal
}

func PutObject(ctx context.Context, to pim.Ref[*caldav.Client], name string, was *caldav.CalendarObject, cal *ical.Calendar) (*caldav.CalendarObject, error) {
	held, etag := "", ""
	if was != nil {
		held, etag = was.Path, was.ETag
	}
	at, ifMatch, ifNoneMatch := pim.Target(to.Path, name, held, etag)
	return to.Client.PutCalendarObject(ctx, at, cal,
		&caldav.PutCalendarObjectOptions{IfMatch: ifMatch, IfNoneMatch: ifNoneMatch})
}

func Join(objects [][]byte) ([]byte, error) {
	var children []*ical.Component
	var left []error
	for _, object := range objects {
		cal, err := ical.NewDecoder(bytes.NewReader(object)).Decode()
		if err != nil {
			left = append(left, err)
			continue
		}
		children = append(children, cal.Children...)
	}
	return Encode(children, left)
}

// WarmAccount reads the login's month around now and its tasks, so the pages open from the mirror.
func (p *Calendars) WarmAccount(ctx context.Context, s *alborz.Login, loc *time.Location) error {
	c, calendars, err := p.Open(ctx, s)
	if err != nil {
		return err
	}
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	events := EventQuery(start.AddDate(0, 0, -7), start.AddDate(0, 1, 0).AddDate(0, 0, 7))
	tasks := TaskQuery()
	type ask struct {
		path  string
		query caldav.CalendarQuery
	}
	var asks []ask
	for _, cal := range calendars {
		if SupportsEvent(cal.Components) {
			asks = append(asks, ask{cal.Path, events})
		}
		if SupportsTodo(cal.Components) {
			asks = append(asks, ask{cal.Path, tasks})
		}
	}
	for _, r := range dav.Each(ctx, asks, func(ctx context.Context, a ask) (int, error) {
		_, err := c.QueryCalendar(ctx, a.path, &a.query)
		return 0, err
	}) {
		if r.Err != nil {
			return r.Err
		}
	}
	return nil
}

// Calendars are every account's calendars and task lists, and the feeds some of them are filled from.
type Calendars struct {
	DAV   *dav.Provider
	feeds *feeds
}

// New finds each served domain's CalDAV server and opens the feeds of the calendars kept in st.
func New(srv *alborz.Server, st *store.Store) (*Calendars, error) {
	provider, err := dav.NewProvider(srv, st, dav.Kind{
		Name: "caldav", Label: "CalDAV",
		Schemes: [2]string{"caldavs", "caldav+insecure"},
		// Invitations and tasks ticked on a phone should show within minutes.
		Poll:     2 * time.Minute,
		Discover: caldav.DiscoverContextURL,
		Own:      func(s dav.Services) string { return s.CalDAV },
		Holds:    store.Calendar,
		FindHome: findHome,
		List:     listCalendars,
		Make:     makeCalendar,
		Change:   changeCalendar,
		Unnamed:  "calendar",
	})
	if err != nil {
		return nil, err
	}
	return &Calendars{DAV: provider, feeds: openFeeds(srv, st)}, nil
}

// Client is the login's CalDAV client.
func (p *Calendars) Client(ctx context.Context, login *alborz.Login) (*caldav.Client, error) {
	return newClient(dav.Base(), p.DAV.HTTPClient(ctx, login))
}

// PRIORITY bands (RFC 5545 3.8.1.9): 1-4 high, 5 medium, 6-9 low, 0 none.
const (
	PriorityHigh   = "high"
	PriorityMedium = "medium"
	PriorityLow    = "low"
)

var PriorityBands = []string{PriorityHigh, PriorityMedium, PriorityLow}

func PriorityBand(todo *ical.Component) string {
	prop := todo.Props.Get(ical.PropPriority)
	if prop == nil {
		return ""
	}
	n, err := strconv.Atoi(prop.Value)
	switch {
	case err != nil || n <= 0:
		return ""
	case n <= 4:
		return PriorityHigh
	case n == 5:
		return PriorityMedium
	default:
		return PriorityLow
	}
}

// A number another client wrote stays while it falls in the chosen band.
func SetPriorityBand(todo *ical.Component, band string) {
	if band == PriorityBand(todo) {
		return
	}
	value := map[string]string{PriorityHigh: "1", PriorityMedium: "5", PriorityLow: "9"}[band]
	if value == "" {
		todo.Props.Del(ical.PropPriority)
		return
	}
	prop := ical.NewProp(ical.PropPriority)
	prop.Value = value
	todo.Props.Set(prop)
}

func MarkTodo(todo *ical.Component, done bool) {
	if done {
		todo.Props.SetText(ical.PropStatus, "COMPLETED")
		todo.Props.SetDateTime(ical.PropCompleted, time.Now().UTC())
	} else {
		todo.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
		todo.Props.Del(ical.PropCompleted)
	}
}

func TaskQuery() caldav.CalendarQuery {
	return caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:  "VCALENDAR",
			Props: []string{"VERSION"},
			// The whole to-do: every column reads a property of it.
			Comps: []caldav.CalendarCompRequest{{Name: "VTODO", AllProps: true, AllComps: true}},
		},
		CompFilter: caldav.CompFilter{
			Name:  "VCALENDAR",
			Comps: []caldav.CompFilter{{Name: "VTODO"}},
		},
	}
}

// AsCopy makes cal an object of its own: a fresh UID, no invitation, and a link to where it came from.
// RFC 9253's LINK with the registered relation "via" names a source; RELATED-TO relates components of
// one calendar, and without a RELTYPE, of which none means a copy, makes the copy the original's child.
func AsCopy(cal *ical.Calendar, uid, source string, now time.Time) {
	cal.Props.Del(ical.PropMethod)
	for _, comp := range cal.Children {
		if comp.Name == ical.CompTimezone {
			continue
		}
		was, _ := comp.Props.Text(ical.PropUID)
		comp.Props.SetText(ical.PropUID, uid)
		comp.Props.SetDateTime(ical.PropDateTimeStamp, now)
		comp.Props.SetDateTime(ical.PropCreated, now)
		comp.Props.Del(ical.PropOrganizer)
		comp.Props.Del(ical.PropAttendee)
		comp.Props.Add(link("URI", source))
		comp.Props.Add(link("UID", was))
	}
}

// The names RFC 9253 gives LINK and its relation parameter.
const (
	propLink     = "LINK"
	paramLinkRel = "LINKREL"
)

func link(kind, value string) *ical.Prop {
	prop := ical.NewProp(propLink)
	prop.Params.Set(ical.ParamValue, kind)
	prop.Params.Set(paramLinkRel, "via")
	prop.Value = value
	return prop
}
