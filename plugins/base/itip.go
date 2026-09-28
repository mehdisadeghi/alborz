package alborzbase

import (
	"bytes"
	"fmt"

	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-smtp"
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

func invitationPart(msg *IMAPMessage) *IMAPPartNode {
	if msg.BodyStructure == nil {
		return nil
	}
	var found *IMAPPartNode
	msg.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		if found != nil {
			return false
		}
		single, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}
		if strings.EqualFold(single.Type, "text") && strings.EqualFold(single.Subtype, "calendar") {
			found = newIMAPPartNode(msg, path, single)
		}
		return true
	})
	return found
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

// SendCalendarMessage posts an iCalendar object as mail (iMIP).
func SendCalendarMessage(ctx *alborz.Context, to []string, subject, method, calendar string) error {
	if len(to) == 0 {
		return nil
	}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	msg := &OutgoingMessage{
		From:           fromAddress(settings, ctx.Login.Username()),
		To:             to,
		Subject:        subject,
		MessageID:      newMessageID(),
		Text:           calendar,
		CalendarMethod: method,
		Mailer:         mailerName(ctx),
	}
	return ctx.DoSMTP(func(c *smtp.Client) error {
		return sendMessage(c, msg)
	})
}

// ShareHeader names the share invitation a mail stands for.
const ShareHeader = "X-Alborz-Share"

// SendText posts a short plain message from the login's account.
func SendText(ctx *alborz.Context, to []string, subject, text string, headers map[string]string) error {
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	msg := &OutgoingMessage{
		From:      fromAddress(settings, ctx.Login.Username()),
		To:        to,
		Subject:   subject,
		MessageID: newMessageID(),
		Text:      text,
		Mailer:    mailerName(ctx),
		Headers:   headers,
	}
	return ctx.DoSMTP(func(c *smtp.Client) error {
		return sendMessage(c, msg)
	})
}

// MethodRequest and MethodCancel are what an organizer sends.
const (
	MethodRequest = methodRequest
	MethodCancel  = methodCancel
)

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
func invitationReply(ctx *alborz.Context, inv *Invitation, status string) (*OutgoingMessage, error) {
	if inv.Organizer == "" {
		return nil, fmt.Errorf("the invitation names no organizer to answer")
	}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return nil, err
	}
	me := ctx.Login.Username()

	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(replyCalendar(inv, me, settings.From, status)); err != nil {
		return nil, fmt.Errorf("failed to write the reply calendar: %v", err)
	}

	return &OutgoingMessage{
		From:           fromAddress(settings, me),
		To:             []string{inv.Organizer},
		Subject:        replySubject(status, inv.Summary),
		MessageID:      newMessageID(),
		Text:           buf.String(),
		CalendarMethod: methodReply,
	}, nil
}
