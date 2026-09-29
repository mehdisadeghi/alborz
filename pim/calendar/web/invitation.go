package web

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/alborzmail/go-itip"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-webdav/caldav"
)

// The invitation is re-read from the message, so what is answered is what was sent.
func handleInvitationReply(ctx *web.Context) error {
	mboxName, uid, err := mail.MessageRef(ctx)
	if err != nil {
		return err
	}

	status := strings.ToUpper(ctx.FormValue("status"))
	switch status {
	case calendar.PartAccepted, calendar.PartDeclined, calendar.PartTentative:
	default:
		return web.NewError(http.StatusBadRequest, "unknown answer")
	}

	request, err := invitationAt(ctx, mboxName, uid)
	if err != nil {
		return err
	}
	if request == nil {
		return web.NewError(http.StatusBadRequest, "this message carries no invitation")
	}
	inv := calendar.ReadInvitation(request, nil)
	if inv.Organizer == "" {
		return web.NewError(http.StatusBadRequest, "the invitation names no organizer to answer")
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
	calendar.MessageZones(reply)

	back := ctx.NextOr(ctx.URL("mail.message", mboxName, uid))
	subject := fmt.Sprintf(ctx.T("invite.subject"+strings.ToLower(status)), inv.Summary)
	if err := mail.SendCalendarMessage(ctx, []string{inv.Organizer}, subject, reply); err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.sendrefused"), err)})
		return ctx.Redirect(http.StatusFound, back)
	}

	ctx.PutNotice(ctx.T("invite.answered"))
	return ctx.Redirect(http.StatusFound, back)
}

// An unparsable invitation is not a page failure: the part is still an attachment.
func messageInvitation(ctx *web.Context, msg *mail.IMAPMessage, mboxName string, uid imap.UID) *calendar.Invitation {
	cal, _, err := mail.CalendarPart(ctx, msg, mboxName, uid)
	if err != nil {
		ctx.Logger().Printf("failed to read the calendar part: %v", err)
		return nil
	}
	if cal == nil {
		return nil
	}
	mine, err := mail.Addresses(ctx.Request().Context(), ctx.Login)
	if err != nil {
		ctx.Logger().Printf("failed to read the account's addresses: %v", err)
		return nil
	}
	return calendar.ReadInvitation(cal, mine)
}

// invitationAt reads the scheduling message one mail carries; nil for none.
func invitationAt(ctx *web.Context, mboxName string, uid imap.UID) (*ical.Calendar, error) {
	cal, _, err := mail.CalendarPartAt(ctx, mboxName, uid)
	return cal, err
}

func sendScheduling(ctx *web.Context, msg *ical.Calendar, to []string) error {
	if len(to) == 0 {
		return nil
	}
	calendar.MessageZones(msg)
	summary, _ := msg.Events()[0].Props.Text(ical.PropSummary)
	subject := summary
	if itip.Method(msg) == itip.MethodCancel {
		subject = ctx.T("invite.cancelledsubject") + ": " + summary
	}
	return mail.SendCalendarMessage(ctx, to, subject, msg)
}

// A meeting this account organizes is withdrawn from its attendees as it goes.
func (p *plugin) deleteEvent(ctx *web.Context, ref pim.Ref[*caldav.Client]) error {
	co, err := ref.Client.GetCalendarObject(ctx.Request().Context(), ref.Path)
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
	if !calendar.Holds(ours, itip.Address(series.Props.Get(ical.PropOrganizer))) {
		return nil
	}
	schedules, err := p.ServerSchedules(ctx.Request().Context(), ctx.LoginFor(ref.Account), ref.Path)
	if err == nil && !schedules {
		err = sendScheduling(ctx, itip.Cancel(co.Data, nil, time.Now()), calendar.Others(itip.Attendees(series), ours))
	}
	if err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.T("invite.cancelfailed")})
		ctx.Logger().Printf("failed to send the cancellation: %v", err)
	}
	return nil
}

func (p *plugin) registerScheduling(g *web.Group) {
	srv := g.Server()
	srv.OnMessage = append(srv.OnMessage, func(ctx *web.Context, _data web.RenderData) error {
		data := _data.(*mail.MessageRenderData)
		inv := messageInvitation(ctx, data.Message, data.Mailbox.Name(), data.Message.UID)
		if inv != nil {
			data.Invitation = inv
		}
		if inv == nil && !hasAttachment(data.Message, calendarTypes...) {
			return nil
		}
		groups, err := p.writableGroups(ctx, calendar.SupportsEvent)
		if err != nil || len(groups) == 0 {
			// No calendar to file it in is not an error on a mail page.
			return nil
		}
		if inv == nil {
			// A calendar with no METHOD is no invitation but still something to file.
			data.ImportCalendars = groups
			return nil
		}
		data.InviteFiled = p.FindByUID(ctx.Request().Context(), ctx.Login, groups, inv.UID)
		if data.InviteFiled == "" && !inv.Cancelled() && !inv.Answer() {
			data.InviteCalendars = groups
		}
		return nil
	})

	g.POST("invite", "/message/{mbox}/{uid}/invite", handleInvitationReply)
	g.POST("fromattachment", "/calendar/from-attachment", p.importCalendar)
	g.POST("frommessage", "/calendar/from-message", p.applyInvitation)
}

// The message is re-read and taken into the copy kept of its event, or filed as a new one.
func (p *plugin) applyInvitation(ctx *web.Context) error {
	mboxName, uid, err := mail.ParseMessageRef(ctx.FormValue("mbox"), ctx.FormValue("uid"))
	if err != nil {
		return err
	}
	msg, err := invitationAt(ctx, mboxName, uid)
	if err != nil {
		return err
	}
	if msg == nil {
		return web.NewError(http.StatusBadRequest, "this message carries no invitation")
	}
	inv := calendar.ReadInvitation(msg, nil)
	back := ctx.NextOr(ctx.URL("calendar.month"))

	groups, err := p.writableGroups(ctx, calendar.SupportsEvent)
	if err != nil {
		return err
	}
	c, _, err := p.Open(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	var stored *caldav.CalendarObject
	if at := p.FindByUID(ctx.Request().Context(), ctx.Login, groups, inv.UID); at != "" {
		if stored, err = c.GetCalendarObject(ctx.Request().Context(), at); err != nil {
			return err
		}
	}
	if stored == nil && (inv.Cancelled() || inv.Answer()) {
		return web.NewError(http.StatusBadRequest, "no calendar holds this event")
	}
	var held *ical.Calendar
	if stored != nil {
		held = stored.Data
	}
	out, err := itip.Apply(held, msg)
	if key := itipRefusal(err); key != "" {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.T(key)})
		return ctx.Redirect(http.StatusFound, back)
	} else if err != nil {
		return err
	}

	if stored != nil {
		if out == nil {
			err = c.RemoveAll(ctx.Request().Context(), stored.Path)
		} else {
			calendar.EnsureTimezones(out, time.Now())
			_, err = c.PutCalendarObject(ctx.Request().Context(), stored.Path, out, nil)
		}
		if err != nil {
			ctx.Notify(pimweb.Refused(ctx, err))
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

	dest, err := p.destination(ctx, ctx.FormValue("calendar"), calendar.SupportsEvent)
	client, calendarPath, account := dest.Client, dest.Path, dest.Account
	if errors.Is(err, pim.ErrNoDestination) {
		return web.NewError(http.StatusBadRequest, "no calendar to file it in")
	} else if err != nil {
		return err
	}
	name := inv.UID
	if name == "" {
		name = uuid.New().String()
	}
	calendar.EnsureTimezones(out, time.Now())
	co, err := client.PutCalendarObject(ctx.Request().Context(),
		path.Join(calendarPath, dav.SafeObjectName(name)+".ics"), out, nil)
	if err != nil {
		ctx.Notify(pimweb.Refused(ctx, err))
		return ctx.Redirect(http.StatusFound, back)
	}
	ctx.PutNotice(ctx.T("invite.filed"))
	if account != "" {
		return ctx.Redirect(http.StatusFound,
			ctx.Path("calendar.event", co.Path)+"?account="+web.QueryValue(account))
	}
	return ctx.Redirect(http.StatusFound, ctx.URL("calendar.event", co.Path))
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

func (p *plugin) importCalendar(ctx *web.Context) error {
	mboxName, uid, err := mail.ParseMessageRef(ctx.FormValue("mbox"), ctx.FormValue("uid"))
	if err != nil {
		return err
	}
	raw, _, err := mail.PartAt(ctx, mboxName, uid, ctx.FormValue("part"))
	if err != nil {
		return err
	}
	dest, err := p.destination(ctx, ctx.FormValue("calendar"), calendar.SupportsEvent)
	client, calendarPath, account := dest.Client, dest.Path, dest.Account
	if errors.Is(err, pim.ErrNoDestination) {
		return web.NewError(http.StatusBadRequest, "no calendar to file it in")
	} else if err != nil {
		return err
	}
	to := ctx.Path("calendar.month")
	if account != "" {
		to += "?account=" + web.QueryValue(account)
	}
	n, err := calendar.Import(ctx.Request().Context(), client, calendarPath, raw)
	if pim.ImportAnswered(err) {
		ctx.Notify(pimweb.ImportRefused(ctx, err))
		return ctx.Redirect(http.StatusFound, ctx.NextOr(to))
	} else if err != nil {
		return err
	}
	if n == 0 {
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("import.nothing")})
	} else {
		ctx.PutNotice(ctx.Tf("import.ics", n))
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr(to))
}
