package alborzbase

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"html/template"
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

// AttachedMessage names a whole message the form carries across a round trip.
type AttachedMessage struct {
	Ref  string
	Name string
}

type ComposeRenderData struct {
	IMAPBaseRenderData
	Message *OutgoingMessage
	// Attached are whole messages carried as message/rfc822 parts.
	Attached []AttachedMessage
	// Identities offered in the From dropdown, one entry per account.
	Identities []AccountIdentities
	// Signatures the account holds, and the one under the message now.
	Signatures []Signature
	Signature  string
	// Suggestions are addresses the recipient fields offer.
	Suggestions []string
}

// AccountIdentities lists the addresses an account may send as, the account itself implied.
type AccountIdentities struct {
	Account   string
	Addresses []string
}

// The address written as is offered even when unlisted, so the dropdown can select it.
func composeIdentities(ctx *alborz.Context, writtenAs string) []AccountIdentities {
	var out []AccountIdentities
	for _, s := range ctx.Logins() {
		entry := AccountIdentities{Account: s.Username()}
		if settings, err := LoadSettings(ctx.Request().Context(), s.Store()); err == nil {
			entry.Addresses = settings.Identities
		}
		if s == ctx.Login && writtenAs != "" && !entry.offers(writtenAs) {
			entry.Addresses = append(entry.Addresses, writtenAs)
		}
		out = append(out, entry)
	}
	return out
}

func (a AccountIdentities) offers(address string) bool {
	want := bareAddress(address)
	if strings.EqualFold(want, a.Account) {
		return true
	}
	for _, identity := range a.Addresses {
		if strings.EqualFold(want, bareAddress(identity)) {
			return true
		}
	}
	return false
}

type messagePath struct {
	Mailbox string
	Uid     imap.UID
}

// attachedField carries "mailbox/uid" refs, re-fetched when the form comes back.
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

// SelectedFrom is the From option the form opens on; only an exact "account" or "account|identity" value selects one.
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

// Uploads are the files the form had taken before it was sent back.
func (d *ComposeRenderData) Uploads() []*formAttachment {
	var out []*formAttachment
	for _, att := range d.Message.Attachments {
		if a, ok := att.(*formAttachment); ok && a.ID != "" {
			out = append(out, a)
		}
	}
	return out
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
	// Signature names the one already under the body.
	Signature string
}

type refused struct {
	key   string
	cause error
}

func (err refused) Error() string { return err.cause.Error() }

// The sender is the From account; the draft and the answered message stay on the request's account.
func (p *plugin) submitCompose(ctx *alborz.Context, sender *alborz.Login, msg *OutgoingMessage, options *composeOptions) error {
	err := sender.DoSMTP(func(c *smtp.Client) error {
		return sendMessage(c, msg)
	})
	if err != nil {
		if _, ok := err.(alborz.AuthError); ok {
			return echo.NewHTTPError(http.StatusForbidden, err)
		}
		return refused{"form.sendrefused", err}
	}

	if inReplyTo := options.InReplyTo; inReplyTo != nil {
		err = ctx.DoIMAP(func(c *imapclient.Client) error {
			return markMessageAnswered(c, inReplyTo.Mailbox, inReplyTo.Uid)
		})
		if err != nil {
			return fmt.Errorf("failed to mark original message as answered: %w", err)
		}
	}

	// A keyword the server may refuse after the send: note the refusal, do not fail the send.
	if forward := options.Forward; forward != nil {
		err = ctx.DoIMAP(func(c *imapclient.Client) error {
			return markMessageForwarded(c, forward.Mailbox, forward.Uid)
		})
		if err != nil {
			ctx.Logger().Printf("failed to mark original message as forwarded: %v", err)
		}
	}

	var sent *MailboxInfo
	var sentUID imap.UID
	err = sender.DoIMAP(func(c *imapclient.Client) error {
		var err error
		sent, sentUID, err = appendMessage(c, msg, "sent")
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

	p.accountChanged(ctx.Login.Username())
	p.accountChanged(sender.Username())
	notice := alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.T("notice.sent")}
	if sent != nil && sentUID != 0 {
		// The Sent copy may be outside the reader's scope, so the link names its account.
		view := messageURL(sent.Name(), sentUID)
		view.RawQuery = "account=" + alborz.QueryValue(sender.Username())
		notice.Actions = []*alborz.NoticeAction{{Label: ctx.T("notice.view"), Path: view.String()}}
	}
	ctx.Notify(notice)
	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/mailbox/INBOX"))
}

func (p *plugin) handleCompose(ctx *alborz.Context, msg *OutgoingMessage, options *composeOptions) error {
	ibase, err := p.newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	msg.Mailer = mailerName(ctx)

	if msg.From == "" && strings.ContainsRune(ctx.Login.Username(), '@') {
		msg.From = fromAddress(settings, ctx.Login.Username())
	}

	if ctx.Request().Method == http.MethodPost {
		formParams, err := ctx.FormParams()
		if err != nil {
			return fmt.Errorf("failed to parse form: %v", err)
		}
		_, saveAsDraft := formParams["save_as_draft"]

		// SMTP, Sent and settings follow the From account; the draft, the answered message and attachments stay on the request's.
		sender := ctx.Login
		account, identity := splitIdentityChoice(ctx.FormValue("from_account"))
		if account != "" && account != sender.Username() {
			sender = ctx.LoginFor(account)
			if sender == nil {
				return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
			}
		}
		settings, err := LoadSettings(ctx.Request().Context(), sender.Store())
		if err != nil {
			return err
		}
		render := func(status int, signature, errText string) error {
			ibase.BaseRenderData.WithTitle(ctx.T("aside.compose"))
			ibase.BaseRenderData.Refused(errText)
			return ctx.Render(status, "compose.html", &ComposeRenderData{
				IMAPBaseRenderData: *ibase,
				Message:            msg,
				Attached:           attachedList(msg),
				Identities:         composeIdentities(ctx, msg.From),
				Signatures:         settings.Signatures,
				Signature:          signature,
				Suggestions:        suggestions(ctx, p.gatherCorrespondents(ctx.Login)),
			})
		}
		// A server's refusal comes back on the form; uploads stay with the session until the message has left.
		var uploads []string
		submit := func(f func() error) error {
			err := f()
			var no refused
			if errors.As(err, &no) {
				return render(http.StatusUnprocessableEntity, ctx.FormValue("signature"), fmt.Sprintf(ctx.T(no.key), no.cause))
			}
			if err == nil {
				for _, id := range uploads {
					if a := ctx.PopAttachment(id); a != nil {
						a.Form.RemoveAll()
					}
				}
			}
			return err
		}
		msg.From = fromAddress(settings, sender.Username())
		// No Sender header: an identity is the same person, not a different transmitter (RFC 5322 3.6.2).
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
		// The text is derived from the HTML so the plain part and the draft agree.
		if msg.HTML = composedHTML(ctx.FormValue("html")); msg.HTML != "" {
			msg.Text = composedText(msg.HTML)
		}

		// Choosing a signature is a submit, so it works without script.
		if _, ok := formParams["signature_apply"]; ok {
			chosen := ctx.FormValue("signature")
			sig, _ := settings.signatureNamed(chosen)
			msg.Text = withSignature(msg.Text, sig.Text)
			if msg.HTML != "" {
				msg.HTML = withSignatureHTML(msg.HTML, sig.Text)
			}
			return render(http.StatusOK, sig.Name, "")
		}
		// Read at send, never from a page flag, which a tab opened earlier can lose.
		msg.SendHTML = msg.HTML == "" && !ctx.Profile().Writing.NoDirectionPart && alborz.HasRTL(msg.Text)
		if msg.SendHTML && options.InReplyTo != nil {
			toList := false
			if err := ctx.DoIMAP(func(c *imapclient.Client) error {
				id, err := messageListID(c, options.InReplyTo.Mailbox, options.InReplyTo.Uid)
				toList = id != ""
				return err
			}); err != nil {
				// Unknown counts as a list: plain text to a person is the smaller harm.
				ctx.Logger().Printf("list check for reply: %v", err)
				toList = true
			}
			msg.SendHTML = !toList
		}
		msg.InReplyTo = ctx.FormValue("in_reply_to")
		msg.MessageID = ctx.FormValue("message_id")
		if msg.MessageID == "" {
			// The page carries the id so a draft saved twice stays one message.
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
			msg.Attachments = append(msg.Attachments, &formAttachment{FileHeader: fh})
		}

		uuids := ctx.FormValue("attachment-uuids")
		for _, uuid := range strings.Split(uuids, ",") {
			if uuid == "" {
				continue
			}

			attachment := ctx.PeekAttachment(uuid)
			if attachment == nil {
				return fmt.Errorf("Unable to retrieve message attachment %s from session", uuid)
			}
			msg.Attachments = append(msg.Attachments,
				&formAttachment{FileHeader: attachment.File, ID: uuid})
			uploads = append(uploads, uuid)
		}

		if msg.HTML != "" {
			images, held, err := inlineImages(ctx, msg.HTML, msg.MessageID)
			if err != nil {
				return err
			}
			msg.Inline = images
			uploads = append(uploads, held...)
		}

		// A draft may be addressed later; a message with nowhere to go is not sent.
		if !saveAsDraft && len(msg.To) == 0 && len(msg.Cc) == 0 && len(msg.Bcc) == 0 {
			return render(http.StatusUnprocessableEntity, ctx.FormValue("signature"), ctx.T("form.recipientneeded"))
		}

		return submit(func() error {
			if saveAsDraft {
				// A draft is what was typed; the editor cannot open a multipart/alternative.
				msg.SendHTML = false
				var (
					drafts *MailboxInfo
					uid    imap.UID
				)
				err = ctx.DoIMAP(func(c *imapclient.Client) error {
					drafts, uid, err = appendMessage(c, msg, "drafts")
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

					// Without UIDPLUS the server names no UID, so the draft is found by Message-ID.
					if uid != 0 {
						return nil
					}
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
					return refused{"form.draftrefused", err}
				}
				p.accountChanged(ctx.Login.Username())
				ctx.PutNotice(ctx.T("notice.draftsaved"))
				return ctx.Redirect(http.StatusFound, fmt.Sprintf(
					"/message/%s/%d/edit?part=1", drafts.Mailbox, uid))
			}
			return p.submitCompose(ctx, sender, msg, options)
		})
	}

	ibase.BaseRenderData.WithTitle(ctx.T("aside.compose"))
	data := &ComposeRenderData{
		Identities:         composeIdentities(ctx, msg.From),
		IMAPBaseRenderData: *ibase,
		Message:            msg,
		Attached:           attachedList(msg),
		Signatures:         settings.Signatures,
		Signature:          options.Signature,
		Suggestions:        suggestions(ctx, p.gatherCorrespondents(ctx.Login)),
	}
	return ctx.Render(http.StatusOK, "compose.html", data)
}

// suggestions adds what other features offer to own, each entry once, sorted.
func suggestions(ctx *alborz.Context, own []string) []string {
	all := slices.Clone(own)
	for _, suggest := range ctx.Server.Suggest {
		all = append(all, suggest(ctx)...)
	}
	var out []string
	seen := make(map[string]bool)
	for _, entry := range all {
		if key := strings.ToLower(entry); !seen[key] {
			seen[key] = true
			out = append(out, entry)
		}
	}
	slices.Sort(out)
	return out
}

// correspondentTTL spares a two-folder read on every compose.
const correspondentTTL = 10 * time.Minute

// Recent is the point: an old correspondent is not what the writer reaches for.
const correspondentDepth = 200

// It never waits: the list is built in the background and the form gets whatever is ready.
func (p *plugin) gatherCorrespondents(s *alborz.Login) []string {
	return p.correspondents.Warm(s.Username(), func() ([]string, error) {
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

		err := s.DoIMAPBackground(func(c *imapclient.Client) error {
			mailboxes, err := listMailboxes(c)
			if err != nil {
				return err
			}
			var categorized CategorizedMailboxes
			for i := range mailboxes {
				categorized.Append(mailboxes[i], nil)
			}
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

func (p *plugin) handleComposeNew(ctx *alborz.Context) error {
	// Only a mailto URI is followed; redirecting to anything else would be an open redirect.
	if uri := ctx.QueryParam("mailto"); uri != "" {
		if to := composeFromMailto(uri); strings.HasPrefix(to, "/compose?") {
			return ctx.Redirect(http.StatusFound, to)
		}
		return echo.NewHTTPError(http.StatusBadRequest, "not a mailto URI")
	}

	text := ctx.QueryParam("body")
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	chosen := settings.DefaultSignature
	if sig, ok := settings.signatureNamed(chosen); ok && text == "" {
		text = withSignature(text, sig.Text)
	} else if !ok {
		chosen = ""
	}

	return p.handleCompose(ctx, &OutgoingMessage{
		From:      ownIdentity(settings, p.newDeliveryTrust(ctx, settings, ctx.Login.Username()), ctx.QueryParam("from")),
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
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": ctx.T("attach.invalid")})
	}
	form, err := reader.ReadForm(alborz.MaxAttachmentSize)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": ctx.T("attach.invalid")})
	}

	var uuids []string
	for _, fh := range form.File["attachments"] {
		uuid, err := ctx.PutAttachment(fh, form)
		if err == alborz.ErrAttachmentCacheSize {
			form.RemoveAll()
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": ctx.T("attach.toolarge")})
		} else if err != nil {
			form.RemoveAll()
			ctx.Logger().Printf("PutAttachment: %v\n", err)
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": ctx.T("attach.failed")})
		}
		uuids = append(uuids, uuid)
	}

	return ctx.JSON(http.StatusOK, &uuids)
}

func handleCancelAttachment(ctx *alborz.Context) error {
	uuid := ctx.Param("uuid")
	a := ctx.PopAttachment(uuid)
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

func (p *plugin) handleReply(ctx *alborz.Context) error {
	var inReplyToPath messagePath
	var err error
	inReplyToPath.Mailbox, inReplyToPath.Uid, err = messageRef(ctx)
	if err != nil {
		return err
	}

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		msg, err = p.populateMessageFromOriginalMessage(ctx, inReplyToPath)
		if err != nil {
			return err
		}
	}

	return p.handleCompose(ctx, &msg, &composeOptions{InReplyTo: &inReplyToPath})
}

// A bare link names no part and a multipart root is not text, so the text part shown is fetched instead.
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

func (p *plugin) populateMessageFromOriginalMessage(ctx *alborz.Context, inReplyToPath messagePath) (OutgoingMessage, error) {
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
		var text string
		if text, err = readText(part); err != nil {
			return ret, err
		}
		quoted, err = quote(strings.NewReader(text))
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

	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return ret, err
	}
	below := ctx.Profile().Writing.ReplyBelowQuote
	ret.Text = replyBody(ctx, inReplyTo, quoted, below)
	ret.QuoteBelow = !below
	if quoted, err := quotedHTML(ctx, inReplyToPath, inReplyTo); err != nil {
		return ret, err
	} else if quoted != "" {
		ret.QuoteHTML = replyHTML(ctx, inReplyTo, quoted, below)
		ret.QuoteRich = true
	}

	ret.MessageID = newMessageID()
	ret.InReplyTo = "<" + inReplyTo.Envelope.MessageID + ">"
	ret.References = strings.TrimSpace(inReplyTo.References + " " + ret.InReplyTo)
	// A mail to an identity is answered by that identity; Cc counts too.
	ret.From = writeAs(settings, p.newDeliveryTrust(ctx, settings, ctx.Login.Username()),
		inReplyTo, inReplyTo.Envelope.To, inReplyTo.Envelope.Cc)
	replyTo := inReplyTo.Envelope.ReplyTo
	if len(replyTo) == 0 {
		replyTo = inReplyTo.Envelope.From
	}
	ret.To = unwrapIMAPAddressList(replyTo)

	if ctx.QueryParam("list") != "" && inReplyTo.ListPost != "" {
		ret.To = []string{inReplyTo.ListPost}
	} else if ctx.QueryParam("all") != "" {
		filtered := filterOutUsername(ctx.Login.Username(),
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

// Not quoted: chevrons on every line are reply convention, not forward.
func forwardBody(ctx *alborz.Context, original *IMAPMessage, body string) string {
	g := alborz.NewBaseRenderData(ctx).GlobalData
	var b strings.Builder
	b.WriteString("\n\n")
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

func forwardHTML(ctx *alborz.Context, original *IMAPMessage, body string) template.HTML {
	head := strings.TrimSpace(forwardBody(ctx, original, ""))
	lines := strings.Split(html.EscapeString(head), "\n")
	return template.HTML(`<p dir="auto"><br></p>` + "\n" +
		`<p dir="` + alborz.ParagraphDir(head) + `">` + strings.Join(lines, "<br>") + "</p>\n<div>" + body + "</div>\n")
}

func quotedHTML(ctx *alborz.Context, path messagePath, msg *IMAPMessage) (string, error) {
	node := msg.HTMLPart()
	if node == nil {
		return "", nil
	}
	var raw []byte
	err := ctx.DoIMAP(func(c *imapclient.Client) error {
		var (
			part *message.Entity
			err  error
		)
		if pieces := msg.HTMLPieces(node.Path); pieces != nil {
			part, err = joinedHTML(c, path.Mailbox, path.Uid, pieces)
		} else {
			_, part, err = getMessagePart(c, path.Mailbox, path.Uid, node.Path)
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
	return composedHTML(localImages(string(raw), msg, ctx.Login.Username())), nil
}

func addressLine(a imap.Address) string {
	if a.Name != "" {
		return fmt.Sprintf("%s <%s>", a.Name, a.Addr())
	}
	return a.Addr()
}

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

func replyHTML(ctx *alborz.Context, original *IMAPMessage, quoted string, below bool) template.HTML {
	who := ""
	if from := original.Envelope.From; len(from) > 0 {
		who = from[0].Name
		if who == "" {
			who = from[0].Addr()
		}
	}
	when := alborz.NewBaseRenderData(ctx).GlobalData.FormatDate(original.Date())
	attribution := ctx.Tf("message.wrote", when, who)
	head := `<p dir="` + alborz.ParagraphDir(attribution) + `">` + html.EscapeString(attribution) + "</p>\n<blockquote>" + quoted + "</blockquote>\n"
	if below {
		return template.HTML(head + `<p dir="auto"><br></p>`)
	}
	return template.HTML(`<p dir="auto"><br></p>` + "\n" + head)
}

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
		return nothingSelected(ctx, back)
	}
	// One message is forwarded inline where it can be trimmed; several travel whole as attachments.
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
		q.Set("account", alborz.QueryValue(a))
	}
	return ctx.Redirect(http.StatusFound, fmt.Sprintf("/message/%v/forward?%s",
		url.PathEscape(mboxName), alborz.Query(q)))
}

// A GET so the page can be reloaded; the selection arrives in the query.
func (p *plugin) handleForwardAttached(ctx *alborz.Context) error {
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
	return p.handleCompose(ctx, &msg, &composeOptions{Attached: paths})
}

func (p *plugin) handleForward(ctx *alborz.Context) error {
	var sourcePath messagePath
	var err error
	sourcePath.Mailbox, sourcePath.Uid, err = messageRef(ctx)
	if err != nil {
		return err
	}

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
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
			body, err = readText(part)
		case "text/html":
			// Lossy; attaching the message instead keeps it whole.
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
		if quoted, err := quotedHTML(ctx, sourcePath, source); err != nil {
			return err
		} else if quoted != "" {
			msg.QuoteHTML = forwardHTML(ctx, source, quoted)
			msg.QuoteRich = true
		}

		msg.MessageID = newMessageID()
		msg.Subject = source.Envelope.Subject
		if !strings.HasPrefix(strings.ToLower(msg.Subject), "fwd:") &&
			!strings.HasPrefix(strings.ToLower(msg.Subject), "fw:") {
			msg.Subject = "Fwd: " + msg.Subject
		}
		// A forward starts a thread of its own rather than joining the original's.
		msg.References = "<" + source.Envelope.MessageID + ">"

		attachments := source.Attachments()
		for i := range attachments {
			msg.Attachments = append(msg.Attachments, &imapAttachment{
				Mailbox: sourcePath.Mailbox,
				Uid:     sourcePath.Uid,
				Node:    &attachments[i],
			})
		}
	}

	return p.handleCompose(ctx, &msg, &composeOptions{Forward: &sourcePath})
}

func (p *plugin) handleEdit(ctx *alborz.Context) error {
	var sourcePath messagePath
	var err error
	sourcePath.Mailbox, sourcePath.Uid, err = messageRef(ctx)
	if err != nil {
		return err
	}

	// TODO: somehow get the path to the In-Reply-To message (with a search?)

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		partPath, err := parsePartPath(ctx.QueryParam("part"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		source, part, mimeType, err := quotablePart(ctx, sourcePath, partPath)
		if err != nil {
			return err
		}
		// A draft with an HTML twin names the alternative; the text inside is what is edited.
		if strings.HasPrefix(mimeType, "multipart/") && source.TextPart() != nil {
			source, part, mimeType, err = quotablePart(ctx, sourcePath, source.TextPart().Path)
			if err != nil {
				return err
			}
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
		if msg.HTML, err = quotedHTML(ctx, sourcePath, source); err != nil {
			return fmt.Errorf("failed to read the draft's HTML: %v", err)
		}

		if len(source.Envelope.From) > 0 {
			msg.From = source.Envelope.From[0].Addr()
		}
		msg.To = unwrapIMAPAddressList(source.Envelope.To)
		msg.Subject = source.Envelope.Subject
		msg.InReplyTo = formatMsgIDList(source.Envelope.InReplyTo)
		msg.MessageID = "<" + source.Envelope.MessageID + ">"

		attachments := source.Attachments()
		for i := range attachments {
			msg.Attachments = append(msg.Attachments, &imapAttachment{
				Mailbox: sourcePath.Mailbox,
				Uid:     sourcePath.Uid,
				Node:    &attachments[i],
			})
		}
	}

	return p.handleCompose(ctx, &msg, &composeOptions{Draft: &sourcePath})
}

func formatMsgIDList(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return "<" + strings.Join(l, ">, <") + ">"
}
