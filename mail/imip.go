package mail

import (
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-smtp"
)

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
