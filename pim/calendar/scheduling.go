package calendar

import (
	"bytes"
	"context"
	"fmt"
	"io"
	netmail "net/mail"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/alborzmail/go-itip"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

// ItipProductID is the PRODID of the calendar objects Alborz writes.
const ItipProductID = "-//mehdix.org//Alborz//EN"

func ParseAttendees(value string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		addr, err := netmail.ParseAddress(line)
		if err != nil {
			continue
		}
		if key := strings.ToLower(addr.Address); !seen[key] {
			seen[key] = true
			out = append(out, addr.Address)
		}
	}
	return out
}

func AttendeeLines(event *ical.Event) string {
	var lines []string
	for i := range event.Props[ical.PropAttendee] {
		prop := &event.Props[ical.PropAttendee][i]
		addr := strings.TrimPrefix(strings.TrimPrefix(prop.Value, "mailto:"), "MAILTO:")
		if addr != "" {
			lines = append(lines, addr)
		}
	}
	return strings.Join(lines, "\n")
}

// Attendees already invited keep their answers; the new ones are asked for one.
func SetScheduling(event *ical.Event, organizer string, attendees []string) {
	was := event.Props[ical.PropAttendee]
	event.Props.Del(ical.PropAttendee)
	if len(attendees) == 0 {
		event.Props.Del(ical.PropOrganizer)
		return
	}
	event.Props.Set(mailto(ical.PropOrganizer, organizer))
	for _, addr := range attendees {
		if i := slices.IndexFunc(was, func(p ical.Prop) bool { return strings.EqualFold(itip.Address(&p), addr) }); i >= 0 {
			event.Props.Add(&was[i])
			continue
		}
		prop := mailto(ical.PropAttendee, addr)
		if strings.EqualFold(addr, organizer) {
			prop.Params.Set("PARTSTAT", PartAccepted)
		} else {
			prop.Params.Set("PARTSTAT", "NEEDS-ACTION")
			prop.Params.Set("RSVP", "TRUE")
		}
		event.Props.Add(prop)
	}
}

func Others(addrs, ours []string) []string {
	var out []string
	for _, addr := range addrs {
		if !Holds(ours, addr) {
			out = append(out, addr)
		}
	}
	return out
}

func Holds(addrs []string, addr string) bool {
	return slices.ContainsFunc(addrs, func(a string) bool { return strings.EqualFold(a, addr) })
}

func mailto(name, addr string) *ical.Prop {
	prop := ical.NewProp(name)
	prop.Value = "mailto:" + addr
	return prop
}

func Clone(cal *ical.Calendar) *ical.Calendar {
	return &ical.Calendar{Component: cloneComponent(cal.Component)}
}

func cloneComponent(comp *ical.Component) *ical.Component {
	out := &ical.Component{Name: comp.Name, Props: make(ical.Props, len(comp.Props))}
	for name, props := range comp.Props {
		for _, prop := range props {
			out.Props[name] = append(out.Props[name], cloneProp(prop))
		}
	}
	for _, child := range comp.Children {
		out.Children = append(out.Children, cloneComponent(child))
	}
	return out
}

func cloneProp(prop ical.Prop) ical.Prop {
	params := make(ical.Params, len(prop.Params))
	for key, values := range prop.Params {
		params[key] = slices.Clone(values)
	}
	return ical.Prop{Name: prop.Name, Params: params, Value: prop.Value}
}

// A server that schedules (RFC 6638 2) tells the attendees itself; alborz telling them too sends everything twice.
func (p *Calendars) ServerSchedules(ctx context.Context, login *alborz.Login, object string) (bool, error) {
	coll := dav.Base().ResolveReference(&url.URL{Path: path.Dir(object) + "/"})
	found, err := dav.Describe(ctx, p.DAV.HTTPClient(ctx, login), coll)
	return found.Has("calendar-auto-schedule"), err
}

// FindByUID is where the login's calendars among groups hold the event uid, empty where none does.
func (p *Calendars) FindByUID(ctx context.Context, login *alborz.Login, groups []dav.Group, uid string) string {
	if uid == "" {
		return ""
	}
	c, _, err := p.Open(ctx, login)
	if err != nil {
		return ""
	}
	query := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:  "VCALENDAR",
			Comps: []caldav.CalendarCompRequest{{Name: "VEVENT", Props: []string{"UID"}}},
		},
		CompFilter: caldav.CompFilter{
			Name: "VCALENDAR",
			Comps: []caldav.CompFilter{{
				Name:  "VEVENT",
				Props: []caldav.PropFilter{{Name: "UID", TextMatch: &caldav.TextMatch{Text: uid, Collation: caldav.CollationOctet}}},
			}},
		},
	}
	for _, group := range groups {
		for _, cal := range group.Collections {
			objects, err := c.QueryCalendar(ctx, cal.Path, query)
			if err != nil {
				continue
			}
			for _, obj := range objects {
				return obj.Path
			}
		}
	}
	return ""
}

// Import reads the whole file before writing, so a refusal can say how much of it was saved.
func Import(ctx context.Context, client *caldav.Client, calendarPath string, raw []byte) (int, error) {
	dec := ical.NewDecoder(bytes.NewReader(raw))
	var calendars []map[string]*ical.Calendar
	total := 0
	for {
		cal, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, &pim.UnreadableError{Err: err}
		}
		objects, err := objectsByUID(cal)
		if err != nil {
			return 0, &pim.UnreadableError{Err: err}
		}
		calendars = append(calendars, objects)
		total += len(objects)
	}
	put := func(uid string, obj *ical.Calendar) error {
		// What go-ical will not write is the file's fault, not the server's.
		if err := ical.NewEncoder(io.Discard).Encode(obj); err != nil {
			return &pim.UnreadableError{Err: err}
		}
		_, err := client.PutCalendarObject(ctx, path.Join(calendarPath, dav.SafeObjectName(uid)+".ics"), obj, nil)
		if err == nil {
			return nil
		}
		// A server refusing a UID it holds under another path: find the object and write there.
		existing := objectPathByUID(ctx, client, calendarPath, uid)
		if existing == "" {
			return err
		}
		_, err = client.PutCalendarObject(ctx, existing, obj, nil)
		return err
	}
	n := 0
	var refused []error
	for _, objects := range calendars {
		for uid, obj := range objects {
			err := put(uid, obj)
			if pim.ObjectRefused(err) {
				refused = append(refused, fmt.Errorf("%s: %w", uid, err))
				continue
			} else if err != nil {
				return n, &pim.PartlySavedError{Saved: n, Total: total, Err: fmt.Errorf("failed to write %s: %w", uid, err)}
			}
			n++
		}
	}
	return pim.Saved(n, total, refused)
}

// A stored object carries no METHOD (RFC 4791 4.1), a DTSTAMP on each component, and names alborz as
// the product that wrote it.
func objectsByUID(cal *ical.Calendar) (map[string]*ical.Calendar, error) {
	for _, child := range cal.Children {
		if child.Name == ical.CompTimezone {
			continue
		}
		if uid, _ := child.Props.Text(ical.PropUID); uid == "" {
			child.Props.SetText(ical.PropUID, uuid.New().String())
		}
		stamp(child)
	}
	objects, err := cal.SplitByUID()
	if err != nil {
		return nil, err
	}
	for _, obj := range objects {
		obj.Props.Del(ical.PropMethod)
		obj.Props.SetText(ical.PropProductID, ItipProductID)
		obj.Props.SetText(ical.PropVersion, "2.0")
	}
	return objects, nil
}

func objectPathByUID(ctx context.Context, client *caldav.Client, calendarPath, uid string) string {
	for _, comp := range []string{"VEVENT", "VTODO"} {
		objects, err := client.QueryCalendar(ctx, calendarPath, &caldav.CalendarQuery{
			CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR",
				Comps: []caldav.CalendarCompRequest{{Name: comp, Props: []string{"UID"}}}},
			CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{
				Name: comp, Props: []caldav.PropFilter{{Name: "UID", TextMatch: &caldav.TextMatch{Text: uid, Collation: caldav.CollationOctet}}},
			}}},
		})
		if err == nil && len(objects) > 0 {
			return objects[0].Path
		}
	}
	return ""
}

func Export(ctx context.Context, client *caldav.Client, calendarPath string, from, to time.Time) ([]byte, error) {
	children, err := Components(ctx, client, calendarPath, []string{"VEVENT", "VTODO"}, from, to)
	if err != nil {
		return nil, err
	}
	// A VCALENDAR holds one component at least (RFC 5545 3.6).
	if len(children) == 0 {
		return nil, pim.ErrNothingToExport
	}
	return Encode(children)
}

func Components(ctx context.Context, client *caldav.Client, calendarPath string, comps []string, from, to time.Time) ([]*ical.Component, error) {
	var out []*ical.Component
	for _, comp := range comps {
		filter := caldav.CompFilter{Name: comp}
		if comp == "VEVENT" && (!from.IsZero() || !to.IsZero()) {
			filter.Start, filter.End = from, to
		}
		objects, err := client.QueryCalendar(ctx, calendarPath, &caldav.CalendarQuery{
			CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
			CompFilter:  caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{filter}},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to read the calendar: %v", err)
		}
		for _, obj := range objects {
			// An export leaves out nothing it does not say.
			if obj.Data == nil {
				return nil, fmt.Errorf("failed to read the calendar: %w", Unparsed("", &obj).Err)
			}
			out = append(out, obj.Data.Children...)
		}
	}
	return out, nil
}

func Encode(children []*ical.Component) ([]byte, error) {
	out := ical.NewCalendar()
	out.Props.SetText(ical.PropProductID, ItipProductID)
	out.Props.SetText(ical.PropVersion, "2.0")
	for _, child := range children {
		// go-ical will not encode an event or task without DTSTAMP; the server's copy is untouched.
		if (child.Name == ical.CompEvent || child.Name == ical.CompToDo) && child.Props.Get(ical.PropDateTimeStamp) == nil {
			stamp := time.Now().UTC()
			if t, err := child.Props.DateTime(ical.PropLastModified, time.UTC); err == nil && !t.IsZero() {
				stamp = t
			}
			child.Props.SetDateTime(ical.PropDateTimeStamp, stamp)
		}
		out.Children = append(out.Children, child)
	}
	out.RemoveDuplicateTimezones()
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(out); err != nil {
		return nil, fmt.Errorf("failed to write the calendar: %v", err)
	}
	return buf.Bytes(), nil
}

const (
	PartAccepted  = "ACCEPTED"
	PartDeclined  = "DECLINED"
	PartTentative = "TENTATIVE"
)

// Invitation is a scheduling request as a page can show it.
type Invitation struct {
	Method    string
	UID       string
	Summary   string
	Location  string
	Start     time.Time
	End       time.Time
	AllDay    bool
	Organizer string
	Attendees []Attendee
	// Mine is this account's own answer so far, empty when not an attendee.
	Mine string
}

// Cancelled reports whether the organizer has withdrawn the meeting.
func (inv *Invitation) Cancelled() bool { return inv.Method == itip.MethodCancel }

// Published reports an event sent to be kept rather than answered.
func (inv *Invitation) Published() bool { return inv.Method == itip.MethodPublish }

// Answer reports whether this is somebody's reply rather than a request.
func (inv *Invitation) Answer() bool { return inv.Method == itip.MethodReply }

// Answered is the attendee whose reply this is.
func (inv *Invitation) Answered() Attendee {
	if len(inv.Attendees) == 0 {
		return Attendee{}
	}
	return inv.Attendees[0]
}

// Attendee is one invited address and what they have answered.
type Attendee struct {
	Name   string
	Addr   string
	Status string
}

func ReadInvitation(cal *ical.Calendar, mine []string) *Invitation {
	event := itip.Series(cal)
	if event == nil {
		event = cal.Events()[0].Component
	}
	inv := &Invitation{Method: itip.Method(cal)}
	inv.UID, _ = event.Props.Text(ical.PropUID)
	inv.Summary, _ = event.Props.Text(ical.PropSummary)
	inv.Location, _ = event.Props.Text(ical.PropLocation)
	inv.Start, _ = (&ical.Event{Component: event}).DateTimeStart(nil)
	inv.End, _ = (&ical.Event{Component: event}).DateTimeEnd(nil)
	if prop := event.Props.Get(ical.PropDateTimeStart); prop != nil {
		inv.AllDay = prop.ValueType() == ical.ValueDate
	}
	if prop := event.Props.Get(ical.PropOrganizer); prop != nil {
		inv.Organizer = itip.Address(prop)
	}
	for i := range event.Props[ical.PropAttendee] {
		prop := &event.Props[ical.PropAttendee][i]
		who := Attendee{
			Name:   prop.Params.Get("CN"),
			Addr:   itip.Address(prop),
			Status: strings.ToUpper(prop.Params.Get("PARTSTAT")),
		}
		inv.Attendees = append(inv.Attendees, who)
		if Holds(mine, who.Addr) {
			inv.Mine = who.Status
		}
	}
	return inv
}
