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
	"github.com/alborzmail/go-itip"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-imap/v2"
	"github.com/labstack/echo/v4"
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

func readInvitation(cal *ical.Calendar, mine []string) *Invitation {
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
		if holds(mine, who.Addr) {
			inv.Mine = who.Status
		}
	}
	return inv
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

	request, err := invitationAt(ctx, mboxName, uid)
	if err != nil {
		return err
	}
	if request == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "this message carries no invitation")
	}
	inv := readInvitation(request, nil)
	if inv.Organizer == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "the invitation names no organizer to answer")
	}
	mine, err := mail.Addresses(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	// Someone the organizer did not invite answers as themselves (RFC 5546 3.2.3).
	me := itip.Invited(request, mine)
	if me == "" {
		me = ctx.Login.Username()
	}
	reply, err := itip.Reply(request, me, status, time.Now())
	if err != nil {
		return err
	}
	messageZones(reply)
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(reply); err != nil {
		return fmt.Errorf("failed to write the reply calendar: %v", err)
	}

	back := ctx.NextOr(ctx.AccountPath(fmt.Sprintf("/message/%s/%v", url.PathEscape(mboxName), uid)))
	subject := fmt.Sprintf(ctx.T("invite.subject"+strings.ToLower(status)), inv.Summary)
	if err := mail.SendCalendarMessage(ctx, []string{inv.Organizer}, subject, itip.MethodReply, buf.String()); err != nil {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.sendrefused"), err)})
		return ctx.Redirect(http.StatusFound, back)
	}

	ctx.PutNotice(ctx.T("invite.answered"))
	return ctx.Redirect(http.StatusFound, back)
}

// An unparsable invitation is not a page failure: the part is still an attachment.
func messageInvitation(ctx *alborz.Context, msg *mail.IMAPMessage, mboxName string, uid imap.UID) *Invitation {
	raw, _, err := mail.CalendarPart(ctx, msg, mboxName, uid)
	if err != nil {
		ctx.Logger().Printf("failed to read the calendar part: %v", err)
		return nil
	}
	if raw == nil {
		return nil
	}
	cal, err := itip.Parse(raw)
	if err != nil {
		return nil
	}
	mine, err := mail.Addresses(ctx.Request().Context(), ctx.Login)
	if err != nil {
		ctx.Logger().Printf("failed to read the account's addresses: %v", err)
		return nil
	}
	return readInvitation(cal, mine)
}

// invitationAt reads the scheduling message one mail carries; nil for none.
func invitationAt(ctx *alborz.Context, mboxName string, uid imap.UID) (*ical.Calendar, error) {
	raw, _, err := mail.CalendarPartAt(ctx, mboxName, uid)
	if err != nil || raw == nil {
		return nil, err
	}
	cal, err := itip.Parse(raw)
	if err != nil {
		return nil, nil
	}
	return cal, nil
}
