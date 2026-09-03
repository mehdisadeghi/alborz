package alborzbase

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-smtp"
	"github.com/labstack/echo/v4"
	"jaytaylor.com/html2text"
)

// AttachedMessage names a whole message the form carries, for the
// checkbox that keeps it across a round trip.
type AttachedMessage struct {
	Ref  string
	Name string
}

type ComposeRenderData struct {
	IMAPBaseRenderData
	Message *OutgoingMessage
	// Attached are whole messages carried as message/rfc822 parts,
	// which have no part path in the message being written.
	Attached []AttachedMessage
	Error    string
	// Identities offered in the From dropdown, one entry per account.
	Identities []AccountIdentities
	// Signatures the account holds, and the one under the message now.
	Signatures []Signature
	Signature  string
}

// AccountIdentities lists the addresses an account may send as: the
// account itself is implied and not repeated in Addresses.
type AccountIdentities struct {
	Account   string
	Addresses []string
}

// composeIdentities reads each signed-in account's stored identities. A
// server that cannot be reached contributes its address alone rather
// than failing the page.
func composeIdentities(ctx *alborz.Context) []AccountIdentities {
	var out []AccountIdentities
	for _, s := range ctx.Sessions() {
		entry := AccountIdentities{Account: s.Username()}
		if settings, err := LoadSettings(s.Store()); err == nil {
			entry.Addresses = settings.Identities
		}
		out = append(out, entry)
	}
	return out
}

type messagePath struct {
	Mailbox string
	Uid     imap.UID
}

// attachedMessages is the compose form's record of whole messages
// carried as attachments: "mailbox/uid", one per entry, re-fetched when
// the form comes back rather than held in memory between requests.
const attachedField = "attached_messages"

func parseAttachedRef(s string) (messagePath, error) {
	i := strings.LastIndex(s, "/")
	if i < 0 {
		return messagePath{}, fmt.Errorf("bad attached message %q", s)
	}
	var p messagePath
	var err error
	p.Mailbox, p.Uid, err = parseMboxAndUid(s[:i], s[i+1:])
	return p, err
}

// PartAttachments are the attachments that came from a part of another
// message, which is the only kind the form can carry back by path. A
// whole message has no part path and rides in Attached instead.
// SelectedFrom is the value of the From option the form opens on: the
// identity the message is written as, matched by address, or the
// account the page belongs to when the message names none. The options
// are worth "account" or "account|identity", and only an exact value
// selects one; comparing the message's From string against them left
// nothing selected, and a browser then shows the first option, which
// on a page for any account but the first was somebody else's.
func (d *ComposeRenderData) SelectedFrom() string {
	fallback := d.GlobalData.Username
	if d.GlobalData.URLAccount != "" {
		fallback = d.GlobalData.URLAccount
	}
	want := bareAddress(d.Message.From)
	if want == "" {
		return fallback
	}
	for _, group := range d.Identities {
		if strings.EqualFold(want, group.Account) {
			return group.Account
		}
		for _, identity := range group.Addresses {
			if strings.EqualFold(want, bareAddress(identity)) {
				return group.Account + "|" + identity
			}
		}
	}
	return fallback
}

func (d *ComposeRenderData) PartAttachments() []*imapAttachment {
	var out []*imapAttachment
	for _, att := range d.Message.Attachments {
		if a, ok := att.(*imapAttachment); ok {
			out = append(out, a)
		}
	}
	return out
}

// attachedList names what the form should carry back, reading the
// attachments already built rather than fetching or remembering
// anything.
func attachedList(msg *OutgoingMessage) []AttachedMessage {
	var out []AttachedMessage
	for _, att := range msg.Attachments {
		if m, ok := att.(*messageAttachment); ok {
			out = append(out, AttachedMessage{Ref: attachedRef(m.Path), Name: m.Name})
		}
	}
	return out
}

func attachedRef(p messagePath) string {
	return fmt.Sprintf("%s/%v", url.PathEscape(p.Mailbox), p.Uid)
}

// attachMessages fetches each named message whole and hangs it off the
// outgoing one.
func attachMessages(ctx *alborz.Context, msg *OutgoingMessage, paths []messagePath) error {
	for _, p := range paths {
		var body []byte
		var env *imap.Envelope
		err := ctx.DoIMAP(func(c *imapclient.Client) error {
			var err error
			body, env, err = fetchRawMessage(c, p.Mailbox, p.Uid)
			return err
		})
		if err != nil {
			return err
		}
		subject := ""
		if env != nil {
			subject = env.Subject
		}
		msg.Attachments = append(msg.Attachments, &messageAttachment{
			Path: p, Name: attachmentName(subject), Body: body,
		})
	}
	return nil
}

type composeOptions struct {
	Draft   *messagePath
	Forward *messagePath
	// Attached are whole messages carried as message/rfc822 parts.
	Attached  []messagePath
	InReplyTo *messagePath
	// Signature names the one already under the body, so the page opens
	// with the right entry chosen rather than with none.
	Signature string
}

// Send message, append it to the Sent mailbox, mark the original message as
// answered. The sender is the account chosen in the From dropdown; the
// draft and the message being answered belong to the request's own
// account, whose mailbox and UID the URL named.
func submitCompose(ctx *alborz.Context, sender *alborz.Session, msg *OutgoingMessage, options *composeOptions) error {
	err := sender.DoSMTP(func(c *smtp.Client) error {
		return sendMessage(c, msg)
	})
	if err != nil {
		if _, ok := err.(alborz.AuthError); ok {
			return echo.NewHTTPError(http.StatusForbidden, err)
		}
		return fmt.Errorf("failed to send message: %w", err)
	}

	if inReplyTo := options.InReplyTo; inReplyTo != nil {
		err = ctx.DoIMAP(func(c *imapclient.Client) error {
			return markMessageAnswered(c, inReplyTo.Mailbox, inReplyTo.Uid)
		})
		if err != nil {
			return fmt.Errorf("failed to mark original message as answered: %w", err)
		}
	}

	// The row shows a forwarded mark that nothing here ever set. Unlike
	// \Answered this is a keyword, which a server is free to refuse, and
	// the message has already been sent: note the refusal, do not fail
	// the send over it.
	if forward := options.Forward; forward != nil {
		err = ctx.DoIMAP(func(c *imapclient.Client) error {
			return markMessageForwarded(c, forward.Mailbox, forward.Uid)
		})
		if err != nil {
			ctx.Logger().Printf("failed to mark original message as forwarded: %v", err)
		}
	}

	err = sender.DoIMAP(func(c *imapclient.Client) error {
		_, err := appendMessage(c, msg, "sent")
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to save message to Sent mailbox: %w", err)
	}

	if draft := options.Draft; draft != nil {
		err = ctx.DoIMAP(func(c *imapclient.Client) error {
			return deleteMessage(c, draft.Mailbox, draft.Uid)
		})
		if err != nil {
			return fmt.Errorf("failed to delete draft: %w", err)
		}
	}

	listings.evictAll(ctx.Session.Username())
	listings.evictAll(sender.Username())
	ctx.Session.PutNotice(ctx.T("notice.sent"))
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/mailbox/INBOX"))
}

func handleCompose(ctx *alborz.Context, msg *OutgoingMessage, options *composeOptions) error {
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	// Both the sent copy and the saved draft carry it, so a message says
	// what wrote it however it left here.
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	msg.Mailer = mailerName(ctx)

	if msg.From == "" && strings.ContainsRune(ctx.Session.Username(), '@') {
		msg.From = fromAddress(settings, ctx.Session.Username())
	}

	if ctx.Request().Method == http.MethodPost {
		formParams, err := ctx.FormParams()
		if err != nil {
			return fmt.Errorf("failed to parse form: %v", err)
		}
		_, saveAsDraft := formParams["save_as_draft"]

		// The From dropdown picks the sending account and, within it, the
		// address to send as. SMTP, the Sent folder and the settings the
		// message is written under follow that account. Everything the
		// URL and the form name - the draft, the message answered, the
		// attachments - stays on the request's own account.
		sender := ctx.Session
		account, identity := splitIdentityChoice(ctx.FormValue("from_account"))
		if account != "" && account != sender.Username() {
			sender = ctx.SessionFor(account)
			if sender == nil {
				return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
			}
		}
		settings, err := LoadSettings(sender.Store())
		if err != nil {
			return err
		}
		render := func(status int, signature, errText string) error {
			ibase.BaseRenderData.WithTitle(ctx.T("aside.compose"))
			return ctx.Render(status, "compose.html", &ComposeRenderData{
				IMAPBaseRenderData: *ibase,
				Message:            msg,
				Attached:           attachedList(msg),
				Identities:         composeIdentities(ctx),
				Signatures:         settings.Signatures,
				Signature:          signature,
				Error:              errText,
			})
		}
		msg.From = fromAddress(settings, sender.Username())
		// An identity replaces the address, and carries its own name when
		// it names one. A stale choice falls back to the account itself.
		//
		// No Sender header. RFC 5322 3.6.2 asks for one when the author
		// and the transmitter are different parties - a secretary
		// sending for someone else - which an identity is not: it is
		// the same person under another of their own addresses. Adding
		// it prints the account address on every message and makes Mail
		// and Outlook render "account on behalf of identity", which is
		// the one thing the identity exists to avoid. DMARC aligns on
		// From, so nothing is bought by it either.
		if identity != "" && slices.Contains(settings.Identities, identity) {
			msg.From = identity
		}
		for _, field := range []struct {
			name string
			into *[]string
		}{{"to", &msg.To}, {"cc", &msg.Cc}, {"bcc", &msg.Bcc}, {"reply_to", &msg.ReplyTo}} {
			addresses, err := parseAddressList(ctx.FormValue(field.name))
			if err != nil {
				return render(http.StatusUnprocessableEntity, ctx.FormValue("signature"), ctx.T("form.recipientinvalid"))
			}
			*field.into = addresses
		}
		msg.Subject = ctx.FormValue("subject")
		msg.Text = ctx.FormValue("text")

		// Choosing a signature is a submit like any other, so it works
		// with no script: the body comes back with the old one replaced
		// and everything else as it was typed.
		if _, ok := formParams["signature_apply"]; ok {
			chosen := ctx.FormValue("signature")
			sig, _ := settings.signatureNamed(chosen)
			msg.Text = withSignature(msg.Text, sig.Text)
			return render(http.StatusOK, sig.Name, "")
		}
		// Both halves of this are the server's to know: the account's
		// setting, and whether the message being answered came from a
		// list. Neither is asked of the page - a form field carrying a
		// decision is one the reader's browser can lose or change, and
		// it made the feature depend on when a tab happened to load.
		msg.SendHTML = settings.SendHTML
		if msg.SendHTML && options.InReplyTo != nil {
			toList := false
			if err := ctx.DoIMAP(func(c *imapclient.Client) error {
				id, err := messageListID(c, options.InReplyTo.Mailbox, options.InReplyTo.Uid)
				toList = id != ""
				return err
			}); err != nil {
				// Unknown is treated as a list: sending plain text to a
				// person is a smaller harm than sending an alternative
				// part to a list that refuses it.
				ctx.Logger().Printf("list check for reply: %v", err)
				toList = true
			}
			msg.SendHTML = !toList
		}
		msg.InReplyTo = ctx.FormValue("in_reply_to")
		msg.MessageID = ctx.FormValue("message_id")
		if msg.MessageID == "" {
			// The page carries the id so a draft saved twice stays one
			// message; a form that lost it is still a message to send.
			msg.MessageID = newMessageID()
		}

		form, err := ctx.MultipartForm()
		if err != nil {
			return fmt.Errorf("failed to get multipart form: %v", err)
		}

		var attached []messagePath
		for _, s := range form.Value[attachedField] {
			p, err := parseAttachedRef(s)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, err)
			}
			attached = append(attached, p)
		}
		if err := attachMessages(ctx, msg, attached); err != nil {
			return err
		}
		options.Attached = attached

		// Fetch previous attachments from original message
		var original *messagePath
		if options.Draft != nil {
			original = options.Draft
		} else if options.Forward != nil {
			original = options.Forward
		}
		if original != nil {
			for _, s := range form.Value["prev_attachments"] {
				path, err := parsePartPath(s)
				if err != nil {
					return fmt.Errorf("failed to parse original attachment path: %v", err)
				}

				var part *message.Entity
				err = ctx.DoIMAP(func(c *imapclient.Client) error {
					var err error
					_, part, err = getMessagePart(c, original.Mailbox, original.Uid, path)
					return err
				})
				if err != nil {
					return fmt.Errorf("failed to fetch attachment from original message: %w", err)
				}

				var buf bytes.Buffer
				if _, err := io.Copy(&buf, part.Body); err != nil {
					return fmt.Errorf("failed to copy attachment from original message: %v", err)
				}

				h := mail.AttachmentHeader{Header: part.Header}
				mimeType, _, _ := h.ContentType()
				filename, _ := h.Filename()
				msg.Attachments = append(msg.Attachments, &imapAttachment{
					Mailbox: original.Mailbox,
					Uid:     original.Uid,
					Node: &IMAPPartNode{
						Path:     path,
						MIMEType: mimeType,
						Filename: filename,
					},
					Body: buf.Bytes(),
				})
			}
		} else if len(form.Value["prev_attachments"]) > 0 {
			return fmt.Errorf("previous attachments specified but no original message available")
		}

		for _, fh := range form.File["attachments"] {
			msg.Attachments = append(msg.Attachments, &formAttachment{fh})
		}

		uuids := ctx.FormValue("attachment-uuids")
		for _, uuid := range strings.Split(uuids, ",") {
			if uuid == "" {
				continue
			}

			attachment := ctx.Session.PopAttachment(uuid)
			if attachment == nil {
				return fmt.Errorf("Unable to retrieve message attachment %s from session", uuid)
			}
			msg.Attachments = append(msg.Attachments,
				&formAttachment{attachment.File})
			defer attachment.Form.RemoveAll()
		}

		// A message with nowhere to go is not sent, and is not reported
		// as sent; a draft may of course be addressed later.
		if !saveAsDraft && len(msg.To) == 0 && len(msg.Cc) == 0 && len(msg.Bcc) == 0 {
			return render(http.StatusUnprocessableEntity, ctx.FormValue("signature"), ctx.T("form.recipientneeded"))
		}

		if saveAsDraft {
			// A draft is what was typed, and only that. The HTML part is
			// a rendering made when the message is sent, so storing it
			// here would put a multipart/alternative in front of the
			// editor - which cannot open one - for no gain.
			msg.SendHTML = false
			var (
				drafts *MailboxInfo
				uid    imap.UID
			)
			err = ctx.DoIMAP(func(c *imapclient.Client) error {
				drafts, err = appendMessage(c, msg, "drafts")
				if err != nil {
					return err
				}

				if draft := options.Draft; draft != nil {
					if err := deleteMessage(c, draft.Mailbox, draft.Uid); err != nil {
						return err
					}
				}

				if err := ensureMailboxSelected(c, drafts.Name()); err != nil {
					return err
				}

				// TODO: use APPENDUID instead when available
				criteria := imap.SearchCriteria{
					Header: []imap.SearchCriteriaHeaderField{
						{Key: "Message-Id", Value: msg.MessageID},
					},
				}
				if data, err := c.UIDSearch(&criteria, nil).Wait(); err != nil {
					return err
				} else if uids := data.AllUIDs(); len(uids) == 0 {
					return fmt.Errorf("the saved draft was not found by its Message-ID")
				} else if len(uids) > 1 {
					return fmt.Errorf("%d drafts carry the Message-ID %s", len(uids), msg.MessageID)
				} else {
					uid = uids[0]
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("failed to save message to Draft mailbox: %w", err)
			}
			listings.evictAll(ctx.Session.Username())
			ctx.Session.PutNotice(ctx.T("notice.draftsaved"))
			return ctx.Redirect(http.StatusFound, fmt.Sprintf(
				"/message/%s/%d/edit?part=1", drafts.Mailbox, uid))
		} else {
			return submitCompose(ctx, sender, msg, options)
		}
	}

	ibase.BaseRenderData.WithTitle(ctx.T("aside.compose"))
	data := &ComposeRenderData{
		Identities:         composeIdentities(ctx),
		IMAPBaseRenderData: *ibase,
		Message:            msg,
		Attached:           attachedList(msg),
		Signatures:         settings.Signatures,
		Signature:          options.Signature,
	}
	if data.Extra == nil {
		data.Extra = make(map[string]interface{})
	}
	data.Extra["EmailSuggestions"] = gatherCorrespondents(ctx.Session)
	return ctx.Render(http.StatusOK, "compose.html", data)
}

// correspondentTTL decides when the gathered addresses count as stale
// and a background pass goes to fetch them again. They change
// only when mail is sent or arrives, and re-reading two folders on every
// compose would put a fetch in front of a blank page.
const correspondentTTL = 10 * time.Minute

// correspondentDepth is how far back the two folders are read. Recent is
// the point - an address nobody has written to in a thousand messages is
// not what the writer is reaching for.
const correspondentDepth = 200

var correspondents = alborz.NewMemo[[]string](correspondentTTL)

// gatherCorrespondents returns the addresses this account has written to
// and heard from. A contact list holds the people you decided to keep;
// this holds the people you actually exchange mail with, which is not
// the same set and is the one a recipient field is usually reaching for.
// Every client collects it - Apple Mail calls them Previous Recipients,
// Thunderbird a Collected Addresses book, Gmail Other Contacts.
//
// It never waits. Reading two folders is far too much to put in front of
// a blank compose form, so the list is built in the background and the
// form gets whatever is ready - on a cold session, nothing.
func gatherCorrespondents(s *alborz.Session) []string {
	return correspondents.Warm(s.Username(), func() ([]string, error) {
		seen := make(map[string]string)
		keep := func(addrs []imap.Address) {
			for _, a := range addrs {
				addr := a.Addr()
				if addr == "" || !strings.ContainsRune(addr, '@') {
					continue
				}
				entry := addr
				if a.Name != "" {
					entry = (&mail.Address{Name: a.Name, Address: addr}).String()
				}
				// A named form wins over a bare one for the same address.
				key := strings.ToLower(addr)
				if old, ok := seen[key]; !ok || len(entry) > len(old) {
					seen[key] = entry
				}
			}
		}

		err := s.DoIMAP(func(c *imapclient.Client) error {
			mailboxes, err := listMailboxes(c)
			if err != nil {
				return err
			}
			var categorized CategorizedMailboxes
			for i := range mailboxes {
				categorized.Append(mailboxes[i], nil)
			}
			// Sent names who you write to, the inbox who writes to you.
			read := func(name string, out bool) {
				msgs, err := recentEnvelopes(c, name, correspondentDepth)
				if err != nil {
					return
				}
				for _, msg := range msgs {
					if msg.Envelope == nil {
						continue
					}
					if out {
						keep(msg.Envelope.To)
						keep(msg.Envelope.Cc)
					} else {
						keep(msg.Envelope.From)
					}
				}
			}
			if sent := categorized.Common.Sent; sent != nil {
				read(sent.Info.Name(), true)
			}
			read("INBOX", false)
			return nil
		})
		if err != nil {
			return nil, err
		}

		out := make([]string, 0, len(seen))
		for _, entry := range seen {
			out = append(out, entry)
		}
		slices.Sort(out)
		return out, nil
	})
}

// withSignature replaces whatever sits under the last "-- " line with
// the given text, or removes it when the text is empty. The delimiter is
// RFC 3676 4.3 and must start a line, so a quoted "> -- " from the
// message being answered is not mistaken for the writer's own.
func withSignature(body, text string) string {
	if i := strings.LastIndex(body, "\n-- \n"); i >= 0 {
		body = body[:i]
	}
	if text == "" {
		return body
	}
	// Room to write above it, which is the point of starting with one.
	return body + "\n\n\n-- \n" + text
}

func handleComposeNew(ctx *alborz.Context) error {
	// Where a mailto: link arrives when the browser has been told this
	// application opens them. Only a mailto URI is followed:
	// composeFromMailto hands anything else straight back, and
	// redirecting to that would be an open redirect.
	if uri := ctx.QueryParam("mailto"); uri != "" {
		if to := composeFromMailto(uri); strings.HasPrefix(to, "/compose?") {
			return ctx.Redirect(http.StatusFound, to)
		}
		return echo.NewHTTPError(http.StatusBadRequest, "not a mailto URI")
	}

	text := ctx.QueryParam("body")
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	chosen := settings.DefaultSignature
	if sig, ok := settings.signatureNamed(chosen); ok && text == "" {
		text = withSignature(text, sig.Text)
	} else if !ok {
		chosen = ""
	}

	// These are common mailto URL query parameters
	return handleCompose(ctx, &OutgoingMessage{
		From:      ownIdentity(settings, newDeliveryTrust(ctx, settings, ctx.Session.Username()), ctx.QueryParam("from")),
		To:        strings.Split(ctx.QueryParam("to"), ","),
		Subject:   ctx.QueryParam("subject"),
		MessageID: newMessageID(),
		InReplyTo: ctx.QueryParam("in-reply-to"),
		Text:      text,
	}, &composeOptions{Signature: chosen})
}

func handleComposeAttachment(ctx *alborz.Context) error {
	reader, err := ctx.Request().MultipartReader()
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "Invalid request",
		})
	}
	form, err := reader.ReadForm(32 << 20) // 32 MB
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "Invalid request",
		})
	}

	var uuids []string
	for _, fh := range form.File["attachments"] {
		uuid, err := ctx.Session.PutAttachment(fh, form)
		if err == alborz.ErrAttachmentCacheSize {
			form.RemoveAll()
			return ctx.JSON(http.StatusBadRequest, map[string]string{
				"error": "Your attachments exceed the maximum file size. Remove some and try again.",
			})
		} else if err != nil {
			form.RemoveAll()
			ctx.Logger().Printf("PutAttachment: %v\n", err)
			return ctx.JSON(http.StatusBadRequest, map[string]string{
				"error": "failed to store attachment",
			})
		}
		uuids = append(uuids, uuid)
	}

	return ctx.JSON(http.StatusOK, &uuids)
}

func handleCancelAttachment(ctx *alborz.Context) error {
	uuid := ctx.Param("uuid")
	a := ctx.Session.PopAttachment(uuid)
	if a != nil {
		a.Form.RemoveAll()
	}
	return ctx.JSON(http.StatusOK, nil)
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

func handleReply(ctx *alborz.Context) error {
	var inReplyToPath messagePath
	var err error
	inReplyToPath.Mailbox, inReplyToPath.Uid, err = messageRef(ctx)
	if err != nil {
		return err
	}

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		msg, err = populateMessageFromOriginalMessage(ctx, inReplyToPath)
		if err != nil {
			return err
		}
	}

	return handleCompose(ctx, &msg, &composeOptions{InReplyTo: &inReplyToPath})
}

// quotablePart fetches the part a reply or a forward should quote. A
// bare link names no part and a multipart message's root is not text,
// so the text part a reader was shown is fetched instead - without it,
// answering any message that carries an attachment was a 400.
func quotablePart(ctx *alborz.Context, path messagePath, partPath []int) (*IMAPMessage, *message.Entity, string, error) {
	var msg *IMAPMessage
	var part *message.Entity
	fetch := func(p []int) error {
		return ctx.DoIMAP(func(c *imapclient.Client) error {
			var err error
			msg, part, err = getMessagePart(c, path.Mailbox, path.Uid, p)
			return err
		})
	}
	if err := fetch(partPath); err != nil {
		return nil, nil, "", err
	}
	mimeType, _, err := part.Header.ContentType()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to parse part Content-Type: %v", err)
	}
	if len(partPath) > 0 || !strings.HasPrefix(mimeType, "multipart/") {
		return msg, part, mimeType, nil
	}

	text := msg.TextPart()
	if text == nil {
		return nil, nil, "", echo.NewHTTPError(http.StatusBadRequest,
			fmt.Errorf("message has no text part to quote"))
	}
	if err := fetch(text.Path); err != nil {
		return nil, nil, "", err
	}
	mimeType, _, err = part.Header.ContentType()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to parse part Content-Type: %v", err)
	}
	return msg, part, mimeType, nil
}

func populateMessageFromOriginalMessage(ctx *alborz.Context, inReplyToPath messagePath) (OutgoingMessage, error) {
	var ret OutgoingMessage

	partPath, err := parsePartPath(ctx.QueryParam("part"))
	if err != nil {
		return ret, echo.NewHTTPError(http.StatusBadRequest, err)
	}

	inReplyTo, part, mimeType, err := quotablePart(ctx, inReplyToPath, partPath)
	if err != nil {
		return ret, err
	}

	var quoted string
	switch mimeType {
	case "text/plain":
		quoted, err = quote(part.Body)
	case "text/html":
		var text string
		text, err = html2text.FromReader(part.Body, html2text.Options{})
		if err != nil {
			return ret, err
		}

		quoted, err = quote(strings.NewReader(text))
	default:
		defErr := fmt.Errorf("cannot forward %q part", mimeType)
		err = echo.NewHTTPError(http.StatusBadRequest, defErr)
	}
	if err != nil {
		return ret, err
	}

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return ret, err
	}
	ret.Text = replyBody(ctx, inReplyTo, quoted, settings.ReplyBelowQuote)
	ret.QuoteBelow = !settings.ReplyBelowQuote

	ret.MessageID = newMessageID()
	ret.InReplyTo = "<" + inReplyTo.Envelope.MessageID + ">"
	// RFC 5322 3.6.4: the chain is what was there plus what is being
	// answered, and it is the header threading actually runs on.
	ret.References = strings.TrimSpace(inReplyTo.References + " " + ret.InReplyTo)
	// Answer from the address it was sent to, when that address is one
	// of ours: a mail to an identity is answered by that identity, not
	// by the account behind it. Cc counts as well, since a list often
	// puts the identity there.
	ret.From = writeAs(settings, newDeliveryTrust(ctx, settings, ctx.Session.Username()),
		inReplyTo, inReplyTo.Envelope.To, inReplyTo.Envelope.Cc)
	replyTo := inReplyTo.Envelope.ReplyTo
	if len(replyTo) == 0 {
		replyTo = inReplyTo.Envelope.From
	}
	ret.To = unwrapIMAPAddressList(replyTo)

	// A reply to the list goes to the list and nowhere else, which is
	// what List-Post names and what the convention expects.
	if ctx.QueryParam("list") != "" && inReplyTo.ListPost != "" {
		ret.To = []string{inReplyTo.ListPost}
	} else if ctx.QueryParam("all") != "" {
		filtered := filterOutUsername(ctx.Session.Username(),
			inReplyTo.Envelope.To)
		ret.To = unwrapIMAPAddressList(append(replyTo, filtered...))
		ret.Cc = unwrapIMAPAddressList(inReplyTo.Envelope.Cc)
	}

	ret.Subject = inReplyTo.Envelope.Subject
	if !strings.HasPrefix(strings.ToLower(ret.Subject), "re:") {
		ret.Subject = "Re: " + ret.Subject
	}

	return ret, nil
}

// readAll is the body of a part as it was written.
func readAll(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	return string(b), err
}

// forwardBody is the message being passed on, whole: a separator, the
// headers that say where it came from, then the body exactly as it
// arrived. It is not quoted - "> " in front of every line is reply
// convention, and prefixing a forward is what turned a readable message
// into a wall of chevrons. The person writes above it.
func forwardBody(ctx *alborz.Context, original *IMAPMessage, body string) string {
	g := alborz.NewBaseRenderData(ctx).GlobalData
	var b strings.Builder
	b.WriteString("\n\n")
	// Not translated: nothing parses it, and the recipient may not read
	// the sender's language. The header names below are RFC 5322's for
	// the same reason.
	b.WriteString(ctx.T("message.forwardedblock"))
	b.WriteString("\n")
	if from := original.Envelope.From; len(from) > 0 {
		fmt.Fprintf(&b, "From: %s\n", addressLine(from[0]))
	}
	fmt.Fprintf(&b, "Date: %s\n", g.FormatDate(original.Date()))
	fmt.Fprintf(&b, "Subject: %s\n", original.Envelope.Subject)
	if to := original.Envelope.To; len(to) > 0 {
		fmt.Fprintf(&b, "To: %s\n", strings.Join(unwrapIMAPAddressList(to), ", "))
	}
	b.WriteString("\n")
	b.WriteString(body)
	return b.String()
}

func addressLine(a imap.Address) string {
	if a.Name != "" {
		return fmt.Sprintf("%s <%s>", a.Name, a.Addr())
	}
	return a.Addr()
}

// replyBody puts the quote where the person writing wants to stand
// relative to it: above it, which is what most correspondents expect,
// or below it, which is what a mailing list asks for. Either way the
// quote is introduced, so a reader can see whose words they are without
// scrolling to find out.
func replyBody(ctx *alborz.Context, original *IMAPMessage, quoted string, below bool) string {
	who := ""
	if from := original.Envelope.From; len(from) > 0 {
		who = from[0].Name
		if who == "" {
			who = from[0].Addr()
		}
	}
	when := alborz.NewBaseRenderData(ctx).GlobalData.FormatDate(original.Date())
	attribution := ctx.Tf("message.wrote", when, who)
	if below {
		return attribution + "\n" + quoted + "\n"
	}
	return "\n\n" + attribution + "\n" + quoted
}

// handleForwardSelection is the list toolbar's Forward: the selection
// names the message, where the message page names it in the path.
// Forwarding several at once means attaching each as message/rfc822,
// which is not built yet, so it says so rather than silently forwarding
// one of them.
func handleForwardSelection(ctx *alborz.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	params, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	uids, err := parseUidList(params["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	back := ctx.NextOr(mailboxURL(ctx, mboxName))
	if len(uids) == 0 {
		return ctx.Redirect(http.StatusFound, back)
	}
	// One message is passed on inline, where the writer can trim it.
	// Several cannot be: concatenating them means nothing, so each
	// travels whole as an attachment, which is what every client that
	// offers this does.
	if len(uids) == 1 {
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(
			fmt.Sprintf("/message/%v/%v/forward", url.PathEscape(mboxName), uids[0])))
	}

	refs := make([]string, len(uids))
	for i, uid := range uids {
		refs[i] = fmt.Sprintf("%v", uid)
	}
	q := url.Values{"uids": {strings.Join(refs, ",")}}
	if a := ctx.URLAccount(); a != "" {
		q.Set("account", alborz.AddressParam(a))
	}
	return ctx.Redirect(http.StatusFound, fmt.Sprintf("/message/%v/forward?%s",
		url.PathEscape(mboxName), q.Encode()))
}

// handleForwardAttached composes one message carrying several whole
// ones. It is a GET so the page can be reloaded and so compose sees the
// request it expects; the selection arrives in the query.
func handleForwardAttached(ctx *alborz.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	uids, err := parseUidList(strings.Split(ctx.QueryParam("uids"), ","))
	if err != nil || len(uids) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no messages to forward")
	}
	paths := make([]messagePath, len(uids))
	for i, uid := range uids {
		paths[i] = messagePath{Mailbox: mboxName, Uid: uid}
	}
	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		if err := attachMessages(ctx, &msg, paths); err != nil {
			return err
		}
		msg.MessageID = newMessageID()
		msg.Subject = ctx.Tf("message.forwardcount", len(uids))
		msg.QuoteBelow = true
	}
	return handleCompose(ctx, &msg, &composeOptions{Attached: paths})
}

func handleForward(ctx *alborz.Context) error {
	var sourcePath messagePath
	var err error
	sourcePath.Mailbox, sourcePath.Uid, err = messageRef(ctx)
	if err != nil {
		return err
	}

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		// Populate fields from original message
		partPath, err := parsePartPath(ctx.QueryParam("part"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		source, part, mimeType, err := quotablePart(ctx, sourcePath, partPath)
		if err != nil {
			return err
		}

		var body string
		switch mimeType {
		case "text/plain":
			body, err = readAll(part.Body)
		case "text/html":
			// Lossy, and the only thing a plain-text composer can do
			// with HTML. Attaching the message instead keeps it whole.
			body, err = html2text.FromReader(part.Body, html2text.Options{})
		default:
			defErr := fmt.Errorf("cannot forward %q part", mimeType)
			err = echo.NewHTTPError(http.StatusBadRequest, defErr)
		}
		if err != nil {
			return err
		}
		msg.Text = forwardBody(ctx, source, body)
		msg.QuoteBelow = true

		msg.MessageID = newMessageID()
		msg.Subject = source.Envelope.Subject
		if !strings.HasPrefix(strings.ToLower(msg.Subject), "fwd:") &&
			!strings.HasPrefix(strings.ToLower(msg.Subject), "fw:") {
			msg.Subject = "Fwd: " + msg.Subject
		}
		// A forward starts a thread of its own: it carried the original's
		// In-Reply-To, which made it a sibling reply to whatever that
		// message was answering, in a conversation the reader may not
		// even be in.
		msg.References = "<" + source.Envelope.MessageID + ">"

		attachments := source.Attachments()
		for i := range attachments {
			// No need to populate attachment body here, we just need the
			// metadata
			msg.Attachments = append(msg.Attachments, &imapAttachment{
				Mailbox: sourcePath.Mailbox,
				Uid:     sourcePath.Uid,
				Node:    &attachments[i],
			})
		}
	}

	return handleCompose(ctx, &msg, &composeOptions{Forward: &sourcePath})
}

func handleEdit(ctx *alborz.Context) error {
	var sourcePath messagePath
	var err error
	sourcePath.Mailbox, sourcePath.Uid, err = messageRef(ctx)
	if err != nil {
		return err
	}

	// TODO: somehow get the path to the In-Reply-To message (with a search?)

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		// Populate fields from source message
		partPath, err := parsePartPath(ctx.QueryParam("part"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		source, part, mimeType, err := quotablePart(ctx, sourcePath, partPath)
		if err != nil {
			return err
		}

		if !strings.EqualFold(mimeType, "text/plain") {
			err := fmt.Errorf("cannot edit %q part", mimeType)
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		b, err := io.ReadAll(part.Body)
		if err != nil {
			return fmt.Errorf("failed to read part body: %v", err)
		}
		msg.Text = string(b)

		if len(source.Envelope.From) > 0 {
			msg.From = source.Envelope.From[0].Addr()
		}
		msg.To = unwrapIMAPAddressList(source.Envelope.To)
		msg.Subject = source.Envelope.Subject
		msg.InReplyTo = formatMsgIDList(source.Envelope.InReplyTo)
		msg.MessageID = "<" + source.Envelope.MessageID + ">"

		attachments := source.Attachments()
		for i := range attachments {
			// No need to populate attachment body here, we just need the
			// metadata
			msg.Attachments = append(msg.Attachments, &imapAttachment{
				Mailbox: sourcePath.Mailbox,
				Uid:     sourcePath.Uid,
				Node:    &attachments[i],
			})
		}
	}

	return handleCompose(ctx, &msg, &composeOptions{Draft: &sourcePath})
}

func formatMsgIDList(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return "<" + strings.Join(l, ">, <") + ">"
}
