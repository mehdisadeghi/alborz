package alborzbase

import (
	"bytes"
	"fmt"
	"net/mail"

	gomail "github.com/emersion/go-message/mail"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-smtp"
)

// Scheduling arrives as a text/calendar part carrying a METHOD (iMIP,
// RFC 6047; iTIP, RFC 5546). A client that cannot read one shows a file
// where every other client shows an invitation, and the sender is left
// waiting for an answer that has no way to be given.
//
// Only the methods a reader can act on are recognised. PUBLISH is a
// calendar somebody sent to be looked at and asks nothing; COUNTER and
// DECLINECOUNTER are negotiation this does not do.
const (
	methodRequest = "REQUEST"
	methodCancel  = "CANCEL"
	methodReply   = "REPLY"
	// PUBLISH is what a client sends when it is not doing iTIP
	// scheduling: Apple Mail attaches the event this way when the
	// meeting is not being negotiated. It asks for no answer, but it is
	// still an event the reader was sent and may want to keep.
	methodPublish = "PUBLISH"
)

// Participation is what an attendee has answered, in the PARTSTAT
// spelling iTIP uses on the wire.
const (
	partAccepted  = "ACCEPTED"
	partDeclined  = "DECLINED"
	partTentative = "TENTATIVE"
)

// Invitation is a scheduling request as a page can show it.
type Invitation struct {
	// Method is REQUEST for an invitation and CANCEL for its withdrawal.
	Method     string
	UID        string
	Summary    string
	Location   string
	Descriptio string
	Start      time.Time
	End        time.Time
	AllDay     bool
	Organizer  string
	Attendees  []Attendee
	// Part is where the calendar lives in the message, so an answer can
	// re-read the original rather than trust what a page posted back.
	Part string
	// Mine is this account's own answer so far, empty when the account
	// is not on the attendee list - which happens on a list invitation
	// or when the mail was forwarded.
	Mine string
}

// Cancelled reports whether the organizer has withdrawn the meeting, in
// which case there is nothing to answer.
func (inv *Invitation) Cancelled() bool { return inv.Method == methodCancel }

// Published reports an event sent to be kept rather than answered. No
// reply is expected, so none is offered - but it can still be filed.
func (inv *Invitation) Published() bool { return inv.Method == methodPublish }

// Answer reports whether this is somebody's reply rather than a request.
// An organizer receives these, and what they say is who answered and
// how - there is nothing on one to accept.
func (inv *Invitation) Answer() bool { return inv.Method == methodReply }

// Answered is the attendee whose reply this is: a reply carries exactly
// one (RFC 5546 3.2.3).
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

// invitationPart finds the scheduling part of a message. A calendar
// arrives as text/calendar, and the METHOD parameter on the content type
// is what separates an invitation from a calendar sent to be read.
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

// readInvitation parses a scheduling part. Anything it cannot make sense
// of is not an invitation, which leaves the part shown as the attachment
// it also is.
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
		// COUNTER and DECLINECOUNTER are a negotiation this does not do.
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
	inv.Descriptio, _ = event.Props.Text(ical.PropDescription)
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

// calAddress reads the address out of a CAL-ADDRESS value, which is a
// URI: "mailto:" in every case that reaches mail.
func calAddress(prop *ical.Prop) string {
	value := strings.TrimSpace(prop.Value)
	if i := strings.Index(strings.ToLower(value), "mailto:"); i == 0 {
		return value[len("mailto:"):]
	}
	return value
}

// replyCalendar builds the answer an organizer expects: the same event,
// identified by its UID, carrying this one attendee and nothing else.
// RFC 5546 3.2.3 asks for exactly that - the reply says what one person
// answered, not what the meeting now looks like.
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
	// DTSTART is optional in a reply (RFC 5546 3.2.3) and is carried only
	// so the organizer's client can match on it. In UTC: a TZID would
	// name a VTIMEZONE this object does not carry, which is the one thing
	// a scheduling message must not do.
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

// ItipProductID names what produced a scheduling message, as PRODID
// asks (RFC 5545 3.7.3).
const ItipProductID = "-//mehdix.org//Alborz//EN"

// SendCalendarMessage posts an iCalendar object as mail, which is what
// iMIP is (RFC 6047): the calendar is the body and the method rides the
// content type. The calendar plugin sends invitations and cancellations
// through it, so there is one place that knows how a scheduling message
// is written.
func SendCalendarMessage(ctx *alborz.Context, to []string, subject, method, calendar string) error {
	if len(to) == 0 {
		return nil
	}
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	me := ctx.Session.Username()
	from := me
	if settings.From != "" {
		from = (&mail.Address{Name: settings.From, Address: me}).String()
	}
	var header gomail.Header
	header.GenerateMessageID()
	id, _ := header.MessageID()

	msg := &OutgoingMessage{
		From:           from,
		To:             to,
		Subject:        subject,
		MessageID:      "<" + id + ">",
		Text:           calendar,
		CalendarMethod: method,
		Mailer:         alborz.BrandName,
	}
	if v := ctx.Server.Options.Version; v != "" {
		msg.Mailer += "/" + v
	}
	return ctx.DoSMTP(func(c *smtp.Client) error {
		return sendMessage(c, msg)
	})
}

// MethodRequest and MethodCancel are what an organizer sends: an
// invitation, and its withdrawal.
const (
	MethodRequest = methodRequest
	MethodCancel  = methodCancel
)

// replySubject is what the organizer sees in their list. Every client
// writes the answer in front of the summary, and an organizer scanning
// a folder of replies is reading exactly that word.
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

// invitationReply is the mail that carries the answer. The calendar is
// the message body rather than an attachment, with the method on the
// content type, which is what RFC 6047 2.4 asks for and what makes a
// receiving client act on it rather than file it.
func invitationReply(ctx *alborz.Context, inv *Invitation, status string) (*OutgoingMessage, error) {
	if inv.Organizer == "" {
		return nil, fmt.Errorf("the invitation names no organizer to answer")
	}
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return nil, err
	}
	me := ctx.Session.Username()

	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(replyCalendar(inv, me, settings.From, status)); err != nil {
		return nil, fmt.Errorf("failed to write the reply calendar: %v", err)
	}

	from := me
	if settings.From != "" {
		from = (&mail.Address{Name: settings.From, Address: me}).String()
	}
	var header gomail.Header
	header.GenerateMessageID()
	id, _ := header.MessageID()

	return &OutgoingMessage{
		From:      from,
		To:        []string{inv.Organizer},
		Subject:   replySubject(status, inv.Summary),
		MessageID: "<" + id + ">",
		Text:      buf.String(),
		// The part is the calendar itself; nothing else is said, because
		// the organizer's client reads the calendar and not the prose.
		CalendarMethod: methodReply,
	}, nil
}
