package alborzcaldav

import (
	"bytes"
	"fmt"
	"net/http"
	"net/mail"
	"path"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
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
		if data.Invitation == nil {
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
		objPath, err := parseObjectPath(ctx.FormValue("path"))
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
		cal.Props.SetText(ical.PropProductID, productID)

		client, calendarPath, account, err := p.resolveCreateCalendar(ctx,
			ctx.FormValue("calendar"), CalendarInfo.SupportsEvent)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "no calendar to file it in")
		}

		name := inv.UID
		if name == "" {
			name = uuid.New().String()
		}
		ensureTimezones(cal, time.Now())
		co, err := client.PutCalendarObject(ctx.Request().Context(),
			path.Join(calendarPath, safeObjectName(name)+".ics"), cal)
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
func (p *plugin) findByUID(ctx *alborz.Context, groups []CalendarGroup, uid string) string {
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

// safeObjectName keeps a UID usable as a path segment. A UID is opaque
// and may hold anything; the collection is addressed by URL.
func safeObjectName(uid string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.', r == '@':
			return r
		}
		return '-'
	}, uid)
}
