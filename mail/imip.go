package mail

import (
	"context"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-imap/v2"
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
func SendCalendarMessage(ctx context.Context, login *alborz.Login, to []string, subject string, cal *ical.Calendar) error {
	if len(to) == 0 {
		return nil
	}
	settings, err := LoadSettings(ctx, login.Store())
	if err != nil {
		return err
	}
	msg := &OutgoingMessage{
		From:      FromAddress(settings, login.Username()),
		To:        to,
		Subject:   subject,
		MessageID: NewMessageID(),
		Calendar:  cal,
		Mailer:    Mailer(),
	}
	return send(login, msg)
}

// ShareHeader names the share invitation a mail stands for.
const ShareHeader = "X-Alborz-Share"

// SendText posts a short plain message from the login's account.
func SendText(ctx context.Context, login *alborz.Login, to []string, subject, text string, headers map[string]string) error {
	settings, err := LoadSettings(ctx, login.Store())
	if err != nil {
		return err
	}
	msg := &OutgoingMessage{
		From:      FromAddress(settings, login.Username()),
		To:        to,
		Subject:   subject,
		MessageID: NewMessageID(),
		Text:      text,
		Mailer:    Mailer(),
		Headers:   headers,
	}
	return send(login, msg)
}

// send posts a message no folder keeps.
func send(login *alborz.Login, msg *OutgoingMessage) error {
	body, err := deliver(login, msg)
	if err != nil {
		return err
	}
	return body.Close()
}
