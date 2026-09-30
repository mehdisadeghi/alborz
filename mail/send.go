package mail

import (
	"context"
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
// its draft on account; it says where the Sent copy landed.
func (p *plugin) submit(ctx context.Context, account, sender *alborz.Login, msg *OutgoingMessage, options *composeOptions) (*MailboxInfo, imap.UID, error) {
	body, err := deliver(sender, msg)
	if err != nil {
		return nil, 0, err
	}
	defer body.Close()

	if inReplyTo := options.InReplyTo; inReplyTo != nil {
		err = account.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
			return markMessageAnswered(c, inReplyTo.Mailbox, inReplyTo.Uid)
		})
		if err != nil {
			return nil, 0, fmt.Errorf("failed to mark original message as answered: %w", err)
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
		return nil, 0, fmt.Errorf("failed to save message to Sent mailbox: %w", err)
	}

	if draft := options.Draft; draft != nil {
		err = account.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
			return deleteMessage(c, draft.Mailbox, draft.Uid)
		})
		if err != nil {
			return nil, 0, fmt.Errorf("failed to delete draft: %w", err)
		}
	}

	p.accountChanged(account.Username())
	p.accountChanged(sender.Username())
	return sent, sentUID, nil
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
