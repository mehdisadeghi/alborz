package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

type messagePath struct {
	Mailbox string
	Uid     imap.UID
}

type composeOptions struct {
	Draft   *messagePath
	Forward *messagePath
	// Attached are whole messages carried as message/rfc822 parts.
	Attached  []messagePath
	InReplyTo *messagePath
	// Signature names the one already under the body.
	Signature string
}

// submit delivers msg from sender and files it in Sent, marking what it answers or forwards and dropping
// its draft on account; it says where the Sent copy landed, and to whom the message went.
func (p *plugin) submit(ctx context.Context, account, sender *alborz.Login, msg *OutgoingMessage, options *composeOptions) (*MailboxInfo, imap.UID, []*mail.Address, error) {
	body, err := deliver(sender, msg)
	if err != nil {
		return nil, 0, nil, err
	}
	defer body.Close()

	if inReplyTo := options.InReplyTo; inReplyTo != nil {
		err = account.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
			return markMessageAnswered(c, inReplyTo.Mailbox, inReplyTo.Uid)
		})
		if err != nil {
			return nil, 0, nil, fmt.Errorf("failed to mark original message as answered: %w", err)
		}
	}

	// A keyword the server may refuse after the send: note the refusal, do not fail the send.
	if forward := options.Forward; forward != nil {
		err = account.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
			return markMessageForwarded(c, forward.Mailbox, forward.Uid)
		})
		if err != nil {
			p.server.Logger().Printf("failed to mark original message as forwarded: %v", err)
		}
	}

	var sent *MailboxInfo
	var sentUID imap.UID
	err = sender.DoIMAPTransfer(ctx, func(c *imapclient.Client) error {
		var err error
		sent, sentUID, err = appendMessage(c, body, "sent")
		return err
	})
	if err != nil {
		return nil, 0, nil, fmt.Errorf("failed to save message to Sent mailbox: %w", err)
	}

	if draft := options.Draft; draft != nil {
		err = account.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
			return deleteMessage(c, draft.Mailbox, draft.Uid)
		})
		if err != nil {
			return nil, 0, nil, fmt.Errorf("failed to delete draft: %w", err)
		}
	}

	p.accountChanged(account.Username())
	p.accountChanged(sender.Username())
	to, err := prepareAddressList(slices.Concat(msg.To, msg.Cc, msg.Bcc))
	return sent, sentUID, to, err
}

// SentRecipients are who the account wrote to in its last n sent messages.
func SentRecipients(s *alborz.Login, n int) ([]*mail.Address, error) {
	var out []*mail.Address
	err := s.DoIMAPBackground(func(c *imapclient.Client) error {
		mailboxes, err := listMailboxes(c)
		if err != nil {
			return err
		}
		var categorized CategorizedMailboxes
		for i := range mailboxes {
			categorized.Append(mailboxes[i], nil)
		}
		sent := categorized.Common.Sent
		if sent == nil {
			return nil
		}
		msgs, err := recentEnvelopes(c, sent.Info.Name(), n)
		if err != nil {
			return err
		}
		for _, msg := range msgs {
			if msg.Envelope == nil {
				continue
			}
			for _, a := range slices.Concat(msg.Envelope.To, msg.Envelope.Cc, msg.Envelope.Bcc) {
				out = append(out, &mail.Address{Name: a.Name, Address: a.Addr()})
			}
		}
		return nil
	})
	return out, err
}

// The delimiter must start a line, so a quoted "> -- " is not mistaken for the writer's own.
func withSignature(body, text string) string {
	if i := strings.LastIndex(body, "\n-- \n"); i >= 0 {
		body = body[:i]
	}
	if text == "" {
		return body
	}
	// Room to write above the signature.
	return body + "\n\n\n-- \n" + text
}

func unwrapIMAPAddressList(addrs []imap.Address) []string {
	l := make([]string, len(addrs))
	for i, addr := range addrs {
		l[i] = unwrapIMAPAddress(addr)
	}
	return l
}

func unwrapIMAPAddress(addr imap.Address) string {
	address := addr.Addr()
	if addr.Name != "" {
		address = fmt.Sprintf("%q <%s>", addr.Name, address)
	}
	return address
}

func readText(part *message.Entity) (string, error) {
	b, err := io.ReadAll(part.Body)
	if err != nil {
		return "", err
	}
	_, params, err := part.Header.ContentType()
	if err != nil || !strings.EqualFold(params["format"], "flowed") {
		return string(b), err
	}
	return Unflow(string(b), strings.EqualFold(params["delsp"], "yes")), nil
}

// fileDraft saves the spooled draft in Drafts in place of the one it replaces, if any, and says where it landed.
func fileDraft(c *imapclient.Client, body io.ReadSeeker, messageID string, replaces *messagePath) (drafts *MailboxInfo, uid imap.UID, err error) {
	drafts, uid, err = appendMessage(c, body, "drafts")
	if err != nil {
		return nil, 0, err
	}
	if replaces != nil {
		if err := deleteMessage(c, replaces.Mailbox, replaces.Uid); err != nil {
			return nil, 0, err
		}
	}
	if err := ensureMailboxSelected(c, drafts.Name()); err != nil {
		return nil, 0, err
	}
	// Without UIDPLUS the server names no UID, so the draft is found by Message-ID.
	if uid != 0 {
		return drafts, uid, nil
	}
	criteria := imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{
			{Key: "Message-Id", Value: messageID},
		},
		// The draft it replaces keeps the Message-ID and is only marked deleted.
		NotFlag: []imap.Flag{imap.FlagDeleted},
	}
	data, err := c.UIDSearch(&criteria, nil).Wait()
	if err != nil {
		return nil, 0, err
	}
	switch uids := data.AllUIDs(); len(uids) {
	case 0:
		return nil, 0, fmt.Errorf("the saved draft was not found by its Message-ID")
	case 1:
		return drafts, uids[0], nil
	default:
		return nil, 0, fmt.Errorf("%d drafts carry the Message-ID %s", len(uids), messageID)
	}
}

// Refused is a server's no to a message, which the form shows beside what was typed; Key names it for the reader.
type Refused struct {
	Key string
	Err error
}

func (err Refused) Error() string { return err.Err.Error() }

func (err Refused) Unwrap() error { return err.Err }

// SaveDraft files what was typed in Drafts, in place of the draft it replaces, and says where it landed.
func (p *plugin) SaveDraft(ctx context.Context, login *alborz.Login, msg *OutgoingMessage, replaces *messagePath) (*MailboxInfo, imap.UID, error) {
	// A draft is what was typed; the editor cannot open a multipart/alternative.
	msg.SendHTML = false
	local, done, err := fetchOriginals(msg)
	if err != nil {
		return nil, 0, err
	}
	defer done()
	body, err := spool(local, imapEightBit)
	if err != nil {
		return nil, 0, err
	}
	defer body.Close()
	var (
		drafts *MailboxInfo
		uid    imap.UID
	)
	err = login.DoIMAPTransfer(ctx, func(c *imapclient.Client) error {
		var err error
		drafts, uid, err = fileDraft(c, body, msg.MessageID, replaces)
		return err
	})
	if err != nil {
		return nil, 0, Refused{"form.draftrefused", err}
	}
	p.accountChanged(login.Username())
	return drafts, uid, nil
}

// attachMessages attaches whole messages to msg, named by their subjects; the send fetches them.
func attachMessages(ctx context.Context, login *alborz.Login, msg *OutgoingMessage, paths []messagePath) error {
	for _, p := range paths {
		var env *imap.Envelope
		err := login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
			var err error
			env, err = fetchEnvelope(c, p.Mailbox, p.Uid)
			return err
		})
		if err != nil {
			return err
		}
		msg.Attachments = append(msg.Attachments, &messageAttachment{
			Path: p, Name: attachmentName(env.Subject), transfer: transferOf(ctx, login),
		})
	}
	return nil
}

// attachPart attaches a part of the message a draft or a forward came from.
func attachPart(ctx context.Context, login *alborz.Login, msg *OutgoingMessage, from messagePath, path []int) error {
	var part *imap.BodyStructureSinglePart
	err := login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		var err error
		part, err = singlePart(c, from.Mailbox, from.Uid, path)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to fetch attachment from original message: %w", err)
	}
	msg.Attachments = append(msg.Attachments, &imapAttachment{
		Mailbox:  from.Mailbox,
		Uid:      from.Uid,
		Node:     newIMAPPartNode(nil, path, part),
		Encoding: part.Encoding,
		transfer: transferOf(ctx, login),
	})
	return nil
}

var errNoText = errors.New("message has no text part to quote")

// quotable is the part a reply or a forward quotes: the one named, else the text part shown.
func quotable(ctx context.Context, login *alborz.Login, path messagePath, partPath []int) (*IMAPMessage, *message.Entity, string, error) {
	var msg *IMAPMessage
	var part *message.Entity
	fetch := func(p []int) error {
		return login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
			var err error
			msg, part, err = getShownPart(c, path.Mailbox, path.Uid, p)
			return err
		})
	}
	if len(partPath) == 0 {
		if err := fetch(nil); err != nil {
			return nil, nil, "", err
		}
		text := msg.TextPart()
		if text == nil {
			return nil, nil, "", errNoText
		}
		partPath = text.Path
	}
	if err := fetch(partPath); err != nil {
		return nil, nil, "", err
	}
	mimeType, _, err := part.Header.ContentType()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to parse part Content-Type: %v", err)
	}
	return msg, part, mimeType, nil
}

// quotedHTML is the HTML of msg a reply or a forward quotes, its own images at what href names; "" where it has none.
func quotedHTML(ctx context.Context, login *alborz.Login, path messagePath, msg *IMAPMessage, href func(*IMAPPartNode) string) (string, error) {
	node := msg.HTMLPart()
	if node == nil {
		return "", nil
	}
	var raw []byte
	err := login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		var (
			part *message.Entity
			err  error
		)
		if pieces := msg.HTMLPieces(node.Path); pieces != nil {
			part, err = joinedHTML(c, path.Mailbox, path.Uid, pieces)
		} else {
			_, part, err = getShownPart(c, path.Mailbox, path.Uid, node.Path)
		}
		if err != nil {
			return err
		}
		raw, err = io.ReadAll(part.Body)
		return err
	})
	if err != nil {
		return "", err
	}
	return localImages(string(raw), msg, href), nil
}

// messageListID is the list a message came through, "" for none.
func messageListID(ctx context.Context, login *alborz.Login, path messagePath) (string, error) {
	var id string
	err := login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		var err error
		id, err = listIDOf(c, path.Mailbox, path.Uid)
		return err
	})
	return id, err
}

// ownIdentity is the address compose opens on: the one asked for where the account may write as it.
func (p *plugin) ownIdentity(login *alborz.Login, settings *Settings, want string) string {
	return ownIdentity(settings, p.newDeliveryTrust(login, settings), want)
}

// replyIdentity answers a mail to an identity by that identity; Cc counts too.
func (p *plugin) replyIdentity(login *alborz.Login, settings *Settings, msg *IMAPMessage) string {
	return writeAs(settings, p.newDeliveryTrust(login, settings), msg, msg.Envelope.To, msg.Envelope.Cc)
}
