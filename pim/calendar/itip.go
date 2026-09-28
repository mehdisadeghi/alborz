package calendar

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-imap/v2"
	"github.com/labstack/echo/v4"
)

// Scheduling mail (iMIP, RFC 6047; iTIP, RFC 5546): only methods a reader can act on.
const (
	methodRequest = "REQUEST"
	methodCancel  = "CANCEL"
	methodReply   = "REPLY"
	// PUBLISH asks for no answer, but is still an event the reader may keep.
	methodPublish = "PUBLISH"
)

const (
	partAccepted  = "ACCEPTED"
	partDeclined  = "DECLINED"
	partTentative = "TENTATIVE"
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
	// Part lets an answer re-read the original rather than trust what a page posted back.
	Part string
	// Mine is this account's own answer so far, empty when not an attendee.
	Mine string
}

// Cancelled reports whether the organizer has withdrawn the meeting.
func (inv *Invitation) Cancelled() bool { return inv.Method == methodCancel }

// Published reports an event sent to be kept rather than answered.
func (inv *Invitation) Published() bool { return inv.Method == methodPublish }

// Answer reports whether this is somebody's reply rather than a request.
func (inv *Invitation) Answer() bool { return inv.Method == methodReply }

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

// Anything unparsable is not an invitation and stays shown as an attachment.
func readInvitation(raw []byte, part string, mine string) *Invitation {
	cal, err := ical.NewDecoder(bytes.NewReader(raw)).Decode()
	if err != nil {
		return nil
	}
	method, _ := cal.Props.Text(ical.PropMethod)
	method = strings.ToUpper(method)
	switch method {
	case methodRequest, methodCancel, methodReply, methodPublish:
	default:
		return nil
	}
	events := cal.Events()
	if len(events) == 0 {
		return nil
	}
	event := events[0]

	inv := &Invitation{Method: method, Part: part}
	inv.UID, _ = event.Props.Text(ical.PropUID)
	inv.Summary, _ = event.Props.Text(ical.PropSummary)
	inv.Location, _ = event.Props.Text(ical.PropLocation)
	inv.Start, _ = event.DateTimeStart(nil)
	inv.End, _ = event.DateTimeEnd(nil)
	if prop := event.Props.Get(ical.PropDateTimeStart); prop != nil {
		inv.AllDay = prop.ValueType() == ical.ValueDate
	}
	if prop := event.Props.Get(ical.PropOrganizer); prop != nil {
		inv.Organizer = calAddress(prop)
	}
	for i := range event.Props[ical.PropAttendee] {
		prop := &event.Props[ical.PropAttendee][i]
		addr := calAddress(prop)
		who := Attendee{
			Name:   prop.Params.Get("CN"),
			Addr:   addr,
			Status: strings.ToUpper(prop.Params.Get("PARTSTAT")),
		}
		inv.Attendees = append(inv.Attendees, who)
		if mine != "" && strings.EqualFold(addr, mine) {
			inv.Mine = who.Status
		}
	}
	return inv
}

func calAddress(prop *ical.Prop) string {
	value := strings.TrimSpace(prop.Value)
	if i := strings.Index(strings.ToLower(value), "mailto:"); i == 0 {
		return value[len("mailto:"):]
	}
	return value
}

// A reply says what one person answered, not what the meeting now looks like.
func replyCalendar(inv *Invitation, me, name, status string) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, ItipProductID)
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropMethod, methodReply)

	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, inv.UID)
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	if inv.Summary != "" {
		event.Props.SetText(ical.PropSummary, inv.Summary)
	}
	// In UTC: a TZID would name a VTIMEZONE this object does not carry.
	if !inv.Start.IsZero() {
		if inv.AllDay {
			event.Props.SetDate(ical.PropDateTimeStart, inv.Start)
		} else {
			event.Props.SetDateTime(ical.PropDateTimeStart, inv.Start.UTC())
		}
	}
	if inv.Organizer != "" {
		organizer := ical.NewProp(ical.PropOrganizer)
		organizer.Value = "mailto:" + inv.Organizer
		event.Props.Set(organizer)
	}
	attendee := ical.NewProp(ical.PropAttendee)
	attendee.Value = "mailto:" + me
	attendee.Params.Set("PARTSTAT", status)
	if name != "" {
		attendee.Params.Set("CN", name)
	}
	event.Props.Set(attendee)

	cal.Children = append(cal.Children, event.Component)
	return cal
}

// ItipProductID is the PRODID of the calendar objects Alborz writes.
const ItipProductID = "-//mehdix.org//Alborz//EN"

func replySubject(status, summary string) string {
	word := "Accepted"
	switch status {
	case partDeclined:
		word = "Declined"
	case partTentative:
		word = "Tentative"
	}
	return word + ": " + summary
}

// The calendar is the body, not an attachment, so the receiving client acts on it.
func invitationReply(ctx *alborz.Context, inv *Invitation, status string) (string, error) {
	if inv.Organizer == "" {
		return "", fmt.Errorf("the invitation names no organizer to answer")
	}
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return "", err
	}
	me := ctx.Login.Username()

	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(replyCalendar(inv, me, settings.From, status)); err != nil {
		return "", fmt.Errorf("failed to write the reply calendar: %v", err)
	}
	return buf.String(), nil
}

// The invitation is re-read from the message, so what is answered is what was sent.
func handleInvitationReply(ctx *alborz.Context) error {
	mboxName, uid, err := mail.ParseMessageRef(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	status := strings.ToUpper(ctx.FormValue("status"))
	switch status {
	case partAccepted, partDeclined, partTentative:
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unknown answer")
	}

	inv, _, err := invitationAt(ctx, mboxName, uid)
	if err != nil {
		return err
	}
	if inv == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "this message carries no invitation")
	}

	reply, err := invitationReply(ctx, inv, status)
	if err != nil {
		return err
	}
	back := ctx.NextOr(ctx.AccountPath(fmt.Sprintf("/message/%s/%v", url.PathEscape(mboxName), uid)))
	if err := mail.SendCalendarMessage(ctx, []string{inv.Organizer}, replySubject(status, inv.Summary), methodReply, reply); err != nil {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.sendrefused"), err)})
		return ctx.Redirect(http.StatusFound, back)
	}

	ctx.PutNotice(ctx.T("invite.answered"))
	return ctx.Redirect(http.StatusFound, back)
}

// An unparsable invitation is not a page failure: the part is still an attachment.
func messageInvitation(ctx *alborz.Context, msg *mail.IMAPMessage, mboxName string, uid imap.UID) *Invitation {
	raw, part, err := mail.CalendarPart(ctx, msg, mboxName, uid)
	if err != nil {
		ctx.Logger().Printf("failed to read the calendar part: %v", err)
		return nil
	}
	if raw == nil {
		return nil
	}
	return readInvitation(raw, part, ctx.Login.Username())
}

// invitationAt reads the scheduling part of one message, and the bytes it was written as.
func invitationAt(ctx *alborz.Context, mboxName string, uid imap.UID) (*Invitation, []byte, error) {
	raw, part, err := mail.CalendarPartAt(ctx, mboxName, uid)
	if err != nil || raw == nil {
		return nil, nil, err
	}
	return readInvitation(raw, part, ctx.Login.Username()), raw, nil
}
