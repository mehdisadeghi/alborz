package calendar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	netmail "net/mail"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/alborzmail/go-itip"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/labstack/echo/v4"
)

// ItipProductID is the PRODID of the calendar objects Alborz writes.
const ItipProductID = "-//mehdix.org//Alborz//EN"

func parseAttendees(value string) []string {
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

func attendeeLines(event *ical.Event) string {
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
func setScheduling(event *ical.Event, organizer string, attendees []string) {
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
			prop.Params.Set("PARTSTAT", partAccepted)
		} else {
			prop.Params.Set("PARTSTAT", "NEEDS-ACTION")
			prop.Params.Set("RSVP", "TRUE")
		}
		event.Props.Add(prop)
	}
}

func others(addrs, ours []string) []string {
	var out []string
	for _, addr := range addrs {
		if !holds(ours, addr) {
			out = append(out, addr)
		}
	}
	return out
}

func holds(addrs []string, addr string) bool {
	return slices.ContainsFunc(addrs, func(a string) bool { return strings.EqualFold(a, addr) })
}

func mailto(name, addr string) *ical.Prop {
	prop := ical.NewProp(name)
	prop.Value = "mailto:" + addr
	return prop
}

func cloneCalendar(cal *ical.Calendar) *ical.Calendar {
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

func sendScheduling(ctx *alborz.Context, msg *ical.Calendar, to []string) error {
	if len(to) == 0 {
		return nil
	}
	messageZones(msg)
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(msg); err != nil {
		return fmt.Errorf("failed to write the scheduling object: %v", err)
	}
	method := itip.Method(msg)
	summary, _ := msg.Events()[0].Props.Text(ical.PropSummary)
	subject := summary
	if method == itip.MethodCancel {
		subject = ctx.T("invite.cancelledsubject") + ": " + summary
	}
	return mail.SendCalendarMessage(ctx, to, subject, method, buf.String())
}

// A server that schedules (RFC 6638 2) tells the attendees itself; alborz telling them too sends everything twice.
func (p *plugin) serverSchedules(ctx *alborz.Context, account, object string) (bool, error) {
	login := ctx.LoginFor(account)
	rctx := ctx.Request().Context()
	coll := p.dav.URL(rctx, login).ResolveReference(&url.URL{Path: path.Dir(object) + "/"})
	found, err := dav.Describe(rctx, p.dav.HTTPClient(rctx, login), coll)
	return found.Has("calendar-auto-schedule"), err
}

// A meeting this account organizes is withdrawn from its attendees as it goes.
func (p *plugin) deleteEvent(ctx *alborz.Context, ref pim.Ref[*caldav.Client]) error {
	co, err := getCalendarObject(ctx, ref.Client, ref.Path)
	if err != nil {
		return err
	}
	if err := ref.Client.RemoveAll(ctx.Request().Context(), ref.Path); err != nil {
		return err
	}
	series := itip.Series(co.Data)
	if series == nil || series.Props.Get(ical.PropOrganizer) == nil {
		return nil
	}
	ours, err := mail.Addresses(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	if !holds(ours, itip.Address(series.Props.Get(ical.PropOrganizer))) {
		return nil
	}
	schedules, err := p.serverSchedules(ctx, ref.Account, ref.Path)
	if err == nil && !schedules {
		err = sendScheduling(ctx, itip.Cancel(co.Data, nil, time.Now()), others(itip.Attendees(series), ours))
	}
	if err != nil {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.T("invite.cancelfailed")})
		ctx.Logger().Printf("failed to send the cancellation: %v", err)
	}
	return nil
}

func (p *plugin) registerScheduling(srv *alborz.Server) {
	srv.OnMessage = append(srv.OnMessage, func(ctx *alborz.Context, _data alborz.RenderData) error {
		data := _data.(*mail.MessageRenderData)
		inv := messageInvitation(ctx, data.Message, data.Mailbox.Name(), data.Message.UID)
		if inv != nil {
			data.Invitation = inv
		}
		if inv == nil && !hasAttachment(data.Message, calendarTypes...) {
			return nil
		}
		groups, err := p.writableGroups(ctx, supportsEvent)
		if err != nil || len(groups) == 0 {
			// No calendar to file it in is not an error on a mail page.
			return nil
		}
		if inv == nil {
			// A calendar with no METHOD is no invitation but still something to file.
			data.ImportCalendars = groups
			return nil
		}
		data.InviteFiled = p.findByUID(ctx, groups, inv.UID)
		if data.InviteFiled == "" && !inv.Cancelled() && !inv.Answer() {
			data.InviteCalendars = groups
		}
		return nil
	})

	p.POST("/message/:mbox/:uid/invite", handleInvitationReply)
	p.POST("/calendar/from-attachment", p.importCalendar)
	p.POST("/calendar/from-message", p.applyInvitation)
}

// The message is re-read and taken into the copy kept of its event, or filed as a new one.
func (p *plugin) applyInvitation(ctx *alborz.Context) error {
	mboxName, uid, err := mail.ParseMessageRef(ctx.FormValue("mbox"), ctx.FormValue("uid"))
	if err != nil {
		return err
	}
	msg, err := invitationAt(ctx, mboxName, uid)
	if err != nil {
		return err
	}
	if msg == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "this message carries no invitation")
	}
	inv := readInvitation(msg, nil)
	back := ctx.NextOr(ctx.AccountPath("/calendar"))

	groups, err := p.writableGroups(ctx, supportsEvent)
	if err != nil {
		return err
	}
	c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	var stored *caldav.CalendarObject
	if at := p.findByUID(ctx, groups, inv.UID); at != "" {
		if stored, err = getCalendarObject(ctx, c, at); err != nil {
			return err
		}
	}
	if stored == nil && (inv.Cancelled() || inv.Answer()) {
		return echo.NewHTTPError(http.StatusBadRequest, "no calendar holds this event")
	}
	var held *ical.Calendar
	if stored != nil {
		held = stored.Data
	}
	out, err := itip.Apply(held, msg)
	if key := itipRefusal(err); key != "" {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.T(key)})
		return ctx.Redirect(http.StatusFound, back)
	} else if err != nil {
		return err
	}

	if stored != nil {
		if out == nil {
			err = c.RemoveAll(ctx.Request().Context(), stored.Path)
		} else {
			ensureTimezones(out, time.Now())
			_, err = c.PutCalendarObject(ctx.Request().Context(), stored.Path, out, nil)
		}
		if err != nil {
			ctx.Notify(pim.Refused(ctx, err))
			return ctx.Redirect(http.StatusFound, back)
		}
		switch {
		case inv.Cancelled():
			ctx.PutNotice(ctx.T("invite.forgotten"))
		case inv.Answer():
			ctx.PutNotice(ctx.T("invite.recorded"))
		default:
			ctx.PutNotice(ctx.T("invite.filed"))
		}
		return ctx.Redirect(http.StatusFound, back)
	}

	dest, err := p.destination(ctx, ctx.FormValue("calendar"), supportsEvent)
	client, calendarPath, account := dest.Client, dest.Path, dest.Account
	if errors.Is(err, pim.ErrNoDestination) {
		return echo.NewHTTPError(http.StatusBadRequest, "no calendar to file it in")
	} else if err != nil {
		return err
	}
	name := inv.UID
	if name == "" {
		name = uuid.New().String()
	}
	ensureTimezones(out, time.Now())
	co, err := client.PutCalendarObject(ctx.Request().Context(),
		path.Join(calendarPath, dav.SafeObjectName(name)+".ics"), out, nil)
	if err != nil {
		ctx.Notify(pim.Refused(ctx, err))
		return ctx.Redirect(http.StatusFound, back)
	}
	ctx.PutNotice(ctx.T("invite.filed"))
	if account != "" {
		return ctx.Redirect(http.StatusFound,
			CalendarObject{CalendarObject: co}.URL()+"?account="+alborz.QueryValue(account))
	}
	return ctx.Redirect(http.StatusFound, ctx.AccountPath(CalendarObject{CalendarObject: co}.URL()))
}

func itipRefusal(err error) string {
	switch {
	case errors.Is(err, itip.ErrOrganizer):
		return "invite.notorganizer"
	case errors.Is(err, itip.ErrOutdated):
		return "invite.outdated"
	case errors.Is(err, itip.ErrSuperseded):
		return "invite.superseded"
	case errors.Is(err, itip.ErrUninvited):
		return "invite.uninvited"
	case errors.Is(err, itip.ErrInstance):
		return "invite.noinstance"
	}
	return ""
}

func (p *plugin) findByUID(ctx *alborz.Context, groups []dav.Group, uid string) string {
	if uid == "" {
		return ""
	}
	c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Login)
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
				Props: []caldav.PropFilter{{Name: "UID", TextMatch: &caldav.TextMatch{Text: uid}}},
			}},
		},
	}
	for _, group := range groups {
		for _, cal := range group.Collections {
			objects, err := c.QueryCalendar(ctx.Request().Context(), cal.Path, query)
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

var calendarTypes = []string{"text/calendar", "application/ics"}

func hasAttachment(msg *mail.IMAPMessage, types ...string) bool {
	for _, att := range msg.Attachments() {
		for _, t := range types {
			if strings.EqualFold(att.MIMEType, t) {
				return true
			}
		}
	}
	return false
}

func (p *plugin) importCalendar(ctx *alborz.Context) error {
	mboxName, uid, err := mail.ParseMessageRef(ctx.FormValue("mbox"), ctx.FormValue("uid"))
	if err != nil {
		return err
	}
	raw, _, err := mail.PartAt(ctx, mboxName, uid, ctx.FormValue("part"))
	if err != nil {
		return err
	}
	dest, err := p.destination(ctx, ctx.FormValue("calendar"), supportsEvent)
	client, calendarPath, account := dest.Client, dest.Path, dest.Account
	if errors.Is(err, pim.ErrNoDestination) {
		return echo.NewHTTPError(http.StatusBadRequest, "no calendar to file it in")
	} else if err != nil {
		return err
	}
	n, err := importObjects(ctx.Request().Context(), client, calendarPath, raw)
	if err != nil {
		return err
	}
	if n == 0 {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("import.nothing")})
	} else {
		ctx.PutNotice(ctx.Tf("import.ics", n))
	}
	to := "/calendar"
	if account != "" {
		to += "?account=" + alborz.QueryValue(account)
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr(to))
}

func importObjects(ctx context.Context, client *caldav.Client, calendarPath string, raw []byte) (int, error) {
	dec := ical.NewDecoder(bytes.NewReader(raw))
	n := 0
	for {
		cal, err := dec.Decode()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, fmt.Errorf("failed to read the calendar: %v", err)
		}
		for uid, obj := range objectsByUID(cal) {
			target := path.Join(calendarPath, dav.SafeObjectName(uid)+".ics")
			if _, err := client.PutCalendarObject(ctx, target, obj, nil); err != nil {
				// A server refusing a UID it holds under another path: find the object and write there.
				existing := objectPathByUID(ctx, client, calendarPath, uid)
				if existing == "" {
					return n, fmt.Errorf("failed to write %s: %v", uid, err)
				}
				if _, err := client.PutCalendarObject(ctx, existing, obj, nil); err != nil {
					return n, fmt.Errorf("failed to write %s: %v", uid, err)
				}
			}
			n++
		}
	}
}

func objectsByUID(cal *ical.Calendar) map[string]*ical.Calendar {
	var zones []*ical.Component
	for _, child := range cal.Children {
		if child.Name == ical.CompTimezone {
			zones = append(zones, child)
		}
	}
	out := map[string]*ical.Calendar{}
	for _, child := range cal.Children {
		if child.Name == ical.CompTimezone {
			continue
		}
		uid, _ := child.Props.Text(ical.PropUID)
		if uid == "" {
			uid = uuid.New().String()
			child.Props.SetText(ical.PropUID, uid)
		}
		obj, ok := out[uid]
		if !ok {
			obj = ical.NewCalendar()
			obj.Props.SetText(ical.PropProductID, ItipProductID)
			obj.Props.SetText(ical.PropVersion, "2.0")
			obj.Children = append(obj.Children, zones...)
			out[uid] = obj
		}
		obj.Children = append(obj.Children, child)
	}
	return out
}

func objectPathByUID(ctx context.Context, client *caldav.Client, calendarPath, uid string) string {
	for _, comp := range []string{"VEVENT", "VTODO"} {
		objects, err := client.QueryCalendar(ctx, calendarPath, &caldav.CalendarQuery{
			CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR",
				Comps: []caldav.CalendarCompRequest{{Name: comp, Props: []string{"UID"}}}},
			CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{
				Name: comp, Props: []caldav.PropFilter{{Name: "UID", TextMatch: &caldav.TextMatch{Text: uid}}},
			}}},
		})
		if err == nil && len(objects) > 0 {
			return objects[0].Path
		}
	}
	return ""
}

func exportCalendar(ctx context.Context, client *caldav.Client, calendarPath string, from, to time.Time) ([]byte, error) {
	children, err := calendarComponents(ctx, client, calendarPath, []string{"VEVENT", "VTODO"}, from, to)
	if err != nil {
		return nil, err
	}
	// A VCALENDAR holds one component at least (RFC 5545 3.6).
	if len(children) == 0 {
		return nil, pim.ErrNothingToExport
	}
	return encodeCalendar(children)
}

func calendarComponents(ctx context.Context, client *caldav.Client, calendarPath string, comps []string, from, to time.Time) ([]*ical.Component, error) {
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
			out = append(out, obj.Data.Children...)
		}
	}
	return out, nil
}

func encodeCalendar(children []*ical.Component) ([]byte, error) {
	out := ical.NewCalendar()
	out.Props.SetText(ical.PropProductID, ItipProductID)
	out.Props.SetText(ical.PropVersion, "2.0")
	zones := map[string]bool{}
	for _, child := range children {
		if child.Name == ical.CompTimezone {
			id, _ := child.Props.Text(ical.PropTimezoneID)
			if zones[id] {
				continue
			}
			zones[id] = true
		}
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
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(out); err != nil {
		return nil, fmt.Errorf("failed to write the calendar: %v", err)
	}
	return buf.Bytes(), nil
}
