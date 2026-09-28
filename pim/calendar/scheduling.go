package calendar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	netmail "net/mail"
	"path"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/labstack/echo/v4"
)

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

func setScheduling(event *ical.Event, organizer string, attendees []string) {
	event.Props.Del(ical.PropAttendee)
	if len(attendees) == 0 {
		event.Props.Del(ical.PropOrganizer)
		return
	}
	org := ical.NewProp(ical.PropOrganizer)
	org.Value = "mailto:" + organizer
	event.Props.Set(org)
	for _, addr := range attendees {
		prop := ical.NewProp(ical.PropAttendee)
		prop.Value = "mailto:" + addr
		// The organizer has agreed; everyone else is asked to answer.
		if strings.EqualFold(addr, organizer) {
			prop.Params.Set("PARTSTAT", "ACCEPTED")
		} else {
			prop.Params.Set("PARTSTAT", "NEEDS-ACTION")
			prop.Params.Set("RSVP", "TRUE")
		}
		event.Props.Add(prop)
	}
}

func schedulingMail(event *ical.Event, method string) (string, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, mail.ItipProductID)
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropMethod, method)

	copied := ical.NewComponent(ical.CompEvent)
	for name, props := range event.Props {
		for i := range props {
			copied.Props.Add(&event.Props[name][i])
		}
	}
	cal.Children = append(cal.Children, copied)

	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return "", fmt.Errorf("failed to write the scheduling object: %v", err)
	}
	return buf.String(), nil
}

func sendScheduling(ctx *alborz.Context, event *ical.Event, attendees []string, method string) error {
	organizer := ctx.Login.Username()
	var to []string
	for _, addr := range attendees {
		if !strings.EqualFold(addr, organizer) {
			to = append(to, addr)
		}
	}
	if len(to) == 0 {
		return nil
	}
	body, err := schedulingMail(event, method)
	if err != nil {
		return err
	}
	summary, _ := event.Props.Text(ical.PropSummary)
	subject := summary
	if method == mail.MethodCancel {
		subject = ctx.T("invite.cancelledsubject") + ": " + summary
	}
	return mail.SendCalendarMessage(ctx, to, subject, method, body)
}

func (p *plugin) registerScheduling(srv *alborz.Server) {
	srv.OnMessage = append(srv.OnMessage, func(ctx *alborz.Context, _data alborz.RenderData) error {
		data := _data.(*mail.MessageRenderData)
		if data.Invitation == nil && !hasAttachment(data.Message, calendarTypes...) {
			return nil
		}
		groups, err := p.writableGroups(ctx, supportsEvent)
		if err != nil || len(groups) == 0 {
			// No calendar to file it in is not an error on a mail page.
			return nil
		}
		if data.Invitation == nil {
			// A calendar with no METHOD is no invitation but still something to file.
			data.ImportCalendars = groups
			return nil
		}
		// For a withdrawal, offer to drop the kept copy rather than to file one.
		if data.Invitation.Cancelled() {
			if found := p.findByUID(ctx, groups, data.Invitation.UID); found != "" {
				data.InviteFiled = found
			}
			return nil
		}
		data.InviteCalendars = groups
		return nil
	})

	p.POST("/calendar/forget-invitation", func(ctx *alborz.Context) error {
		objPath, err := dav.ParseObjectPath(ctx.FormValue("path"))
		if err != nil {
			return err
		}
		c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Login)
		if err != nil {
			return err
		}
		if err := c.RemoveAll(ctx.Request().Context(), objPath); err != nil {
			return fmt.Errorf("failed to remove the meeting: %v", err)
		}
		ctx.PutNotice(ctx.T("invite.forgotten"))
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath("/calendar")))
	})

	p.POST("/calendar/from-message", p.importCalendar)

	p.POST("/calendar/from-message", func(ctx *alborz.Context) error {
		mboxName, uid, err := mail.ParseMessageRef(ctx.FormValue("mbox"), ctx.FormValue("uid"))
		if err != nil {
			return err
		}
		inv, raw, err := mail.InvitationAt(ctx, mboxName, uid)
		if err != nil {
			return err
		}
		if inv == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "this message carries no invitation")
		}

		// The organizer's own object is filed, not one rebuilt from the page; only METHOD goes.
		cal, err := ical.NewDecoder(bytes.NewReader(raw)).Decode()
		if err != nil {
			return fmt.Errorf("failed to read the invitation: %v", err)
		}
		cal.Props.Del(ical.PropMethod)
		cal.Props.SetText(ical.PropProductID, mail.ItipProductID)

		dest, err := p.destination(ctx, ctx.FormValue("calendar"), supportsEvent)
		client, calendarPath, account := dest.Client, dest.Path, dest.Account
		if errors.Is(err, dav.ErrNoDestination) {
			return echo.NewHTTPError(http.StatusBadRequest, "no calendar to file it in")
		} else if err != nil {
			return err
		}

		name := inv.UID
		if name == "" {
			name = uuid.New().String()
		}
		ensureTimezones(cal, time.Now())
		co, err := client.PutCalendarObject(ctx.Request().Context(),
			path.Join(calendarPath, dav.SafeObjectName(name)+".ics"), cal, nil)
		if err != nil {
			ctx.Notify(dav.Refused(ctx, err))
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath("/calendar")))
		}

		ctx.PutNotice(ctx.T("invite.filed"))
		if account != "" {
			return ctx.Redirect(http.StatusFound,
				CalendarObject{CalendarObject: co}.URL()+"?account="+alborz.QueryValue(account))
		}
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(CalendarObject{CalendarObject: co}.URL()))
	})
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
	if errors.Is(err, dav.ErrNoDestination) {
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
			obj.Props.SetText(ical.PropProductID, mail.ItipProductID)
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
		return nil, dav.ErrNothingToExport
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
	out.Props.SetText(ical.PropProductID, mail.ItipProductID)
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
