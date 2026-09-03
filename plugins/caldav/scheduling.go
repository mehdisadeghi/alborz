package alborzcaldav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"path"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// Scheduling is two halves of one protocol. Answering an invitation is
// mail's half and lives in the base plugin; putting the meeting in a
// calendar, and sending one of your own, are this plugin's, because both
// need a CalDAV collection to write to.

// parseAttendees reads the addresses a form lists, one per line, the way
// the identity field does. An organizer is not one of them: the account
// sending is the organizer and is written as such.
func parseAttendees(value string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		addr, err := mail.ParseAddress(line)
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

// attendeeLines writes an event's attendees back into the form's shape.
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

// setScheduling puts the organizer and the attendees on an event. The
// organizer is the account doing the sending: iTIP identifies the one
// party who owns the meeting by that property, and a request from
// anybody else is not one the recipients can answer.
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
		// The organizer has agreed by definition; everyone else is being
		// asked, and RSVP says an answer is wanted (RFC 5545 3.2.17).
		if strings.EqualFold(addr, organizer) {
			prop.Params.Set("PARTSTAT", "ACCEPTED")
		} else {
			prop.Params.Set("PARTSTAT", "NEEDS-ACTION")
			prop.Params.Set("RSVP", "TRUE")
		}
		event.Props.Add(prop)
	}
}

// schedulingMail is the iTIP object sent to the attendees: the event as
// stored, with the method on the calendar (RFC 5546 3.2.1).
func schedulingMail(event *ical.Event, method string) (string, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, alborzbase.ItipProductID)
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

// sendScheduling mails the invitation, or its withdrawal, to everyone on
// it. A send that fails must not lose the event: it is already saved, so
// the failure is reported and the meeting stays.
func sendScheduling(ctx *alborz.Context, event *ical.Event, attendees []string, method string) error {
	organizer := ctx.Session.Username()
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
	if method == alborzbase.MethodCancel {
		subject = ctx.T("invite.cancelledsubject") + ": " + summary
	}
	return alborzbase.SendCalendarMessage(ctx, to, subject, method, body)
}

// registerScheduling adds what belongs to the calendar: filing an
// invitation that arrived by mail into a collection the reader chooses.
func (p *plugin) registerScheduling() {
	p.Inject("message.html", func(ctx *alborz.Context, _data alborz.RenderData) error {
		data := _data.(*alborzbase.MessageRenderData)
		if data.Invitation == nil && !hasAttachment(data.Message, calendarTypes...) {
			return nil
		}
		groups, err := p.writableGroups(ctx, CalendarInfo.SupportsEvent)
		if err != nil || len(groups) == 0 {
			// No calendar to file it in is not an error on a mail page.
			return nil
		}
		if data.Extra == nil {
			data.Extra = make(map[string]interface{})
		}
		if data.Invitation == nil {
			// A calendar attached with no METHOD is not an invitation,
			// but it is still something to file, from its own row.
			data.Extra["ImportCalendars"] = groups
			return nil
		}
		// A withdrawal asks the opposite question. Offering to file a
		// meeting the organizer has just called off is the wrong verb,
		// so the page looks for the copy already kept and offers to
		// drop that instead.
		if data.Invitation.Cancelled() {
			if found := p.findByUID(ctx, groups, data.Invitation.UID); found != "" {
				data.Extra["InviteFiled"] = found
			}
			return nil
		}
		data.Extra["InviteCalendars"] = groups
		return nil
	})

	p.POST("/calendar/forget-invitation", func(ctx *alborz.Context) error {
		objPath, err := dav.ParseObjectPath(ctx.FormValue("path"))
		if err != nil {
			return err
		}
		c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}
		if err := c.RemoveAll(ctx.Request().Context(), objPath); err != nil {
			return fmt.Errorf("failed to remove the meeting: %v", err)
		}
		ctx.Session.PutNotice(ctx.T("invite.forgotten"))
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath("/calendar")))
	})

	p.GET("/calendar/import", p.importPage)
	p.POST("/calendar/import", p.importCalendar)

	p.POST("/calendar/from-message", func(ctx *alborz.Context) error {
		mboxName, uid, err := alborzbase.ParseMessageRef(ctx.FormValue("mbox"), ctx.FormValue("uid"))
		if err != nil {
			return err
		}
		inv, raw, err := alborzbase.InvitationAt(ctx, mboxName, uid)
		if err != nil {
			return err
		}
		if inv == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "this message carries no invitation")
		}

		// The organizer's own object is filed, not one rebuilt from what
		// a page displayed: its UID, its recurrence, its alarms. Only
		// METHOD goes, because a stored event is not a message about one.
		cal, err := ical.NewDecoder(bytes.NewReader(raw)).Decode()
		if err != nil {
			return fmt.Errorf("failed to read the invitation: %v", err)
		}
		cal.Props.Del(ical.PropMethod)
		cal.Props.SetText(ical.PropProductID, alborzbase.ItipProductID)

		client, calendarPath, account, err := p.resolveCreateCalendar(ctx,
			ctx.FormValue("calendar"), CalendarInfo.SupportsEvent)
		if errors.Is(err, errUnknownCalendar) {
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
			path.Join(calendarPath, dav.SafeObjectName(name)+".ics"), cal)
		if err != nil {
			return fmt.Errorf("failed to file the invitation: %v", err)
		}

		ctx.Session.PutNotice(ctx.T("invite.filed"))
		if account != "" {
			return ctx.Redirect(http.StatusFound,
				CalendarObject{CalendarObject: co}.URL()+"?account="+alborz.AddressParam(account))
		}
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(CalendarObject{CalendarObject: co}.URL()))
	})
}

// findByUID looks for a stored copy of one event across the calendars
// that can be written to. A UID is unique within a collection (RFC 4791
// 4.1), which is what makes this a lookup rather than a guess.
func (p *plugin) findByUID(ctx *alborz.Context, groups []dav.Group[CalendarInfo], uid string) string {
	if uid == "" {
		return ""
	}
	c, _, err := p.clientWithCalendars(ctx.Request().Context(), ctx.Session)
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

// calendarTypes are the media types a calendar file arrives under.
var calendarTypes = []string{"text/calendar", "application/ics"}

// hasAttachment reports whether the message carries a part of one of
// the media types.
func hasAttachment(msg *alborzbase.IMAPMessage, types ...string) bool {
	for _, att := range msg.Attachments() {
		for _, t := range types {
			if strings.EqualFold(att.MIMEType, t) {
				return true
			}
		}
	}
	return false
}

// ImportRenderData drives calendar-import.html: an address to fetch,
// handed over by the browser or typed, and the calendars to file into.
type ImportRenderData struct {
	alborz.BaseRenderData
	Groups []dav.Group[CalendarInfo]
	URL    string
	Error  string
}

// importPage asks which calendar an address should be filed into. A
// browser registered for webcal: links lands here with the address.
func (p *plugin) importPage(ctx *alborz.Context) error {
	groups, err := p.writableGroups(ctx, CalendarInfo.SupportsEvent)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "calendar-import.html", &ImportRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("import.title")),
		Groups:         groups,
		URL:            ctx.QueryParam("url"),
	})
}

// maxImportSize bounds what is fetched from an address: a calendar of
// a few thousand entries is a few megabytes, and a file past this is
// not one to file blindly.
const maxImportSize = 16 << 20

// importCalendar files a calendar into the chosen one, every object it
// holds. It comes from a mail's attachment, or from an address, which
// is fetched once and forgotten: a subscription that follows the
// address is a different thing, and this is not it.
func (p *plugin) importCalendar(ctx *alborz.Context) error {
	var raw []byte
	if address := ctx.FormValue("url"); address != "" {
		u, err := url.Parse(address)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "not an address")
		}
		// webcal: is https: by another name, which is how a calendar
		// link says it is one.
		if u.Scheme == "webcal" {
			u.Scheme = "https"
		}
		client := alborz.NewRemoteClient(alborz.RoundTripTimeout)
		resp, err := client.Get(u.String())
		if err != nil {
			return fmt.Errorf("failed to fetch the calendar: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("failed to fetch the calendar: %s", resp.Status)
		}
		raw, err = io.ReadAll(io.LimitReader(resp.Body, maxImportSize+1))
		if err != nil {
			return err
		}
		if len(raw) > maxImportSize {
			return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "the calendar is too large to import")
		}
	} else {
		mboxName, uid, err := alborzbase.ParseMessageRef(ctx.FormValue("mbox"), ctx.FormValue("uid"))
		if err != nil {
			return err
		}
		raw, _, err = alborzbase.PartAt(ctx, mboxName, uid, ctx.FormValue("part"))
		if err != nil {
			return err
		}
	}
	client, calendarPath, account, err := p.resolveCreateCalendar(ctx,
		ctx.FormValue("calendar"), CalendarInfo.SupportsEvent)
	if errors.Is(err, errUnknownCalendar) {
		return echo.NewHTTPError(http.StatusBadRequest, "no calendar to file it in")
	} else if err != nil {
		return err
	}
	n, err := importObjects(ctx.Request().Context(), client, calendarPath, raw)
	if err != nil {
		return err
	}
	if n == 0 {
		ctx.Session.PutNotice(ctx.T("import.nothing"))
	} else {
		ctx.Session.PutNotice(ctx.Tf("import.ics", n))
	}
	to := "/calendar"
	if account != "" {
		to += "?account=" + alborz.AddressParam(account)
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr(to))
}

// importObjects writes every object of an iCalendar stream into the
// calendar, one object per UID as CalDAV wants it, and reports how many.
// A UID the calendar already holds is written over in place.
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
			if _, err := client.PutCalendarObject(ctx, target, obj); err != nil {
				// A server that refuses a UID it holds under another
				// path names the object; find it and write there.
				existing := objectPathByUID(ctx, client, calendarPath, uid)
				if existing == "" {
					return n, fmt.Errorf("failed to write %s: %v", uid, err)
				}
				if _, err := client.PutCalendarObject(ctx, existing, obj); err != nil {
					return n, fmt.Errorf("failed to write %s: %v", uid, err)
				}
			}
			n++
		}
	}
}

// objectsByUID splits one calendar into the objects a server stores:
// every component sharing a UID, a series and its overrides included,
// goes together with the timezones the file defines. METHOD does not
// travel: a stored object is not a message about one.
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
			obj.Props.SetText(ical.PropProductID, alborzbase.ItipProductID)
			obj.Props.SetText(ical.PropVersion, "2.0")
			obj.Children = append(obj.Children, zones...)
			out[uid] = obj
		}
		obj.Children = append(obj.Children, child)
	}
	return out
}

// objectPathByUID is where the calendar keeps the object with that UID,
// "" when it has none.
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

// exportCalendar renders every object of the calendar, or those whose
// events fall in the range, as one iCalendar file.
func exportCalendar(ctx context.Context, client *caldav.Client, calendarPath string, from, to time.Time) ([]byte, error) {
	children, err := calendarComponents(ctx, client, calendarPath, []string{"VEVENT", "VTODO"}, from, to)
	if err != nil {
		return nil, err
	}
	return encodeCalendar(children)
}

// calendarComponents reads the calendar's objects of the given kinds,
// events narrowed to the range when one is given, and returns every
// component they hold.
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

// encodeCalendar writes components as one file: the objects' components
// under one VCALENDAR, each timezone defined once.
func encodeCalendar(children []*ical.Component) ([]byte, error) {
	out := ical.NewCalendar()
	out.Props.SetText(ical.PropProductID, alborzbase.ItipProductID)
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
		out.Children = append(out.Children, child)
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(out); err != nil {
		return nil, fmt.Errorf("failed to write the calendar: %v", err)
	}
	return buf.Bytes(), nil
}
