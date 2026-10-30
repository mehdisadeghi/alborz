package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	messagemail "github.com/emersion/go-message/mail"
	"golang.org/x/net/html"
)

// ErrNotificationStale means the notification predates the current mailbox's UID namespace.
var ErrNotificationStale = errors.New("notification mailbox changed")

// NotificationPreview reads a bounded excerpt without marking the message read.
// Attachments, encrypted parts, remote images and links are never opened for a notification.
func NotificationPreview(ctx context.Context, login *alborz.Login, uid imap.UID, validity uint32) (string, error) {
	var preview string
	err := login.DoIMAPWork(ctx, alborz.IMAPBackground, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		var err error
		preview, err = notificationPreview(c, uid, validity)
		return err
	})
	return preview, err
}

const (
	notificationBodyMax   = 16 << 10
	notificationHTMLMax   = 64 << 10
	notificationHeaderMax = 4 << 10
)

func notificationPreview(c *imapclient.Client, uid imap.UID, validity uint32) (string, error) {
	if err := selectNotification(c, "INBOX", validity); err != nil {
		return "", err
	}
	subjectHeader := &imap.FetchItemBodySection{Peek: true, Specifier: imap.PartSpecifierHeader,
		HeaderFields: []string{"Subject"}, Partial: &imap.SectionPartial{Size: notificationHeaderMax}}
	buf, err := fetchOne(c, uid, &imap.FetchOptions{UID: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		BodySection: []*imap.FetchItemBodySection{subjectHeader}})
	if err != nil {
		return "", err
	}
	msg := &IMAPMessage{FetchMessageBuffer: buf, Mailbox: "INBOX"}
	var preview string
	// An overlong or malformed subject may be omitted, without fetching the rest of its header.
	if parsed, err := readHeaderBytes(buf.FindBodySection(subjectHeader)); err == nil {
		header := messagemail.Header{Header: message.Header{Header: parsed}}
		subject, _ := header.Subject()
		preview = notificationText(subject, 100)
	}
	// TextPart prefers plain text and falls back to HTML, excluding attached text files.
	part := msg.TextPart()
	if part == nil {
		return preview, nil
	}
	text, err := notificationPartText(c, msg, part)
	if err != nil {
		return "", err
	}
	// Some multipart/alternative messages include an empty plain-text part.
	// Only try the primary HTML alternative, never an attached text file.
	if fallback := msg.HTMLPart(); text == "" && fallback != nil && !slices.Equal(fallback.Path, part.Path) {
		text, err = notificationPartText(c, msg, fallback)
		if err != nil {
			return "", err
		}
	}
	if text != "" {
		if preview != "" {
			preview += "\n"
		}
		preview += text
	}
	return preview, nil
}

func notificationPartText(c *imapclient.Client, msg *IMAPMessage, part *IMAPPartNode) (string, error) {
	text, err := notificationPartWindow(c, msg, part, notificationBodyMax)
	// A long <head> can fill the first window. Offsets are in encoded bytes, so
	// re-read one larger window from the start rather than seeking past </head>.
	if err == nil && text == "" && part.MIMEType == "text/html" && part.octets > notificationBodyMax {
		text, err = notificationPartWindow(c, msg, part, notificationHTMLMax)
	}
	return text, err
}

func notificationPartWindow(c *imapclient.Client, msg *IMAPMessage, part *IMAPPartNode, size int64) (string, error) {
	section := &imap.FetchItemBodySection{Peek: true, Part: part.Path, Partial: &imap.SectionPartial{Size: size}}
	fetched, err := fetchOne(c, msg.UID, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}})
	if err != nil {
		return "", err
	}
	// BODYSTRUCTURE already supplies the decoding headers; no unbounded root or MIME header fetch.
	var header message.Header
	header.SetContentType(part.MIMEType, part.params)
	msg.BodyStructure.Walk(func(path []int, structure imap.BodyStructure) bool {
		if single, ok := structure.(*imap.BodyStructureSinglePart); ok && slices.Equal(path, part.Path) {
			header.Set("Content-Transfer-Encoding", single.Encoding)
		}
		return true
	})
	raw := fetched.FindBodySection(section)
	entity, err := readPart(header.Header, raw, part, int64(part.octets) > size)
	if err != nil {
		return "", err
	}
	// The fetched part is already bounded. Cutting its decoded HTML again can
	// discard the body after a long stylesheet, leaving a subject-only preview.
	body, err := io.ReadAll(entity.Body)
	if err != nil {
		return "", err
	}
	text := string(body)
	if part.MIMEType == "text/html" {
		text = notificationHTML(text)
	}
	return notificationText(text, 180), nil
}

func selectNotification(c *imapclient.Client, mailbox string, validity uint32) error {
	selected, err := c.Select(mailbox, nil).Wait()
	if err != nil {
		return err
	}
	if selected.UIDValidity != validity {
		return ErrNotificationStale
	}
	return nil
}

// CheckNotification validates a notification link without reading or changing the message.
func CheckNotification(ctx context.Context, login *alborz.Login, mailbox string, uid imap.UID, validity uint32) error {
	return login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		if err := selectNotification(c, mailbox, validity); err != nil {
			return err
		}
		_, err := fetchOne(c, uid, &imap.FetchOptions{UID: true})
		return err
	})
}

func notificationText(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		// Keep joiners used by languages and emoji, while removing bidi controls.
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d') {
			return -1
		}
		return r
	}, text)
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return string(runes)
}

// Unlike html2text, which decorates headings, tables and links for reading, a notification wants bare text.
func notificationHTML(text string) string {
	z := html.NewTokenizer(strings.NewReader(text))
	var out strings.Builder
	var hidden string
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out.String()
		case html.StartTagToken:
			tag, _ := z.TagName()
			if hidden == "" && (string(tag) == "script" || string(tag) == "style" || string(tag) == "head") {
				hidden = string(tag)
			}
		case html.EndTagToken:
			tag, _ := z.TagName()
			if string(tag) == hidden {
				hidden = ""
			}
		case html.TextToken:
			if hidden == "" {
				out.Write(z.Text())
				out.WriteByte(' ')
			}
		}
	}
}

// NotificationAction checks UIDVALIDITY on the same connection that changes the message, so an old
// notification cannot act on a reused UID after a mailbox reset. Delete moves to Trash.
func (p *Mailboxes) NotificationAction(ctx context.Context, login *alborz.Login, mailbox string, uid imap.UID, validity uint32, action string) error {
	return login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		if err := selectNotification(c, mailbox, validity); err != nil {
			return err
		}
		if _, err := fetchOne(c, uid, &imap.FetchOptions{UID: true}); err != nil {
			return err
		}
		switch action {
		case "read":
			return p.writeFlags(c, login.Username(), mailbox, []imap.UID{uid}, imap.StoreFlagsAdd, []imap.Flag{imap.FlagSeen})
		case "delete":
			trash, err := p.resolveRole(c, login.Username(), "Trash")
			if err != nil {
				return err
			}
			if trash == "" || trash == mailbox {
				return fmt.Errorf("no trash folder available")
			}
			_, err = p.moveMessages(c, login.Username(), mailbox, trash, []imap.UID{uid})
			return err
		default:
			return fmt.Errorf("unknown notification action")
		}
	})
}
