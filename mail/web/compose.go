package web

import (
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/lang"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message"
	"jaytaylor.com/html2text"
)

// AttachedMessage names a whole message the form carries across a round trip.
type AttachedMessage struct {
	Ref  string
	Name string
}

type ComposeRenderData struct {
	IMAPBaseRenderData
	Message *mail.OutgoingMessage
	// Attached are whole messages carried as message/rfc822 parts.
	Attached []AttachedMessage
	// Identities offered in the From dropdown, one entry per account.
	Identities []AccountIdentities
	// Signatures the account holds, and the one under the message now.
	Signatures []mail.Signature
	Signature  string
	// Suggestions are addresses the recipient fields offer.
	Suggestions []string
	// Reply opens the text at its end, below the quote.
	Reply bool
	// LocalImage matches the images the editor may keep: those alborz holds (ADR 40).
	LocalImage string
}

// AccountIdentities lists the addresses an account may send as, the account itself implied.
type AccountIdentities struct {
	Account   string
	Addresses []string
}

// The address written as is offered even when unlisted, so the dropdown can select it.
func composeIdentities(ctx *web.Context, writtenAs string) []AccountIdentities {
	var out []AccountIdentities
	for _, s := range ctx.Logins() {
		entry := AccountIdentities{Account: s.Username()}
		if settings, err := mail.LoadSettings(ctx.Request().Context(), s.Store()); err == nil {
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
	want := mail.BareAddress(address)
	if strings.EqualFold(want, a.Account) {
		return true
	}
	for _, identity := range a.Addresses {
		if strings.EqualFold(want, mail.BareAddress(identity)) {
			return true
		}
	}
	return false
}

// attachedField carries "mailbox/uid" refs, re-fetched when the form comes back.
const attachedField = "attached_messages"

func parseAttachedRef(s string) (mail.MessagePath, error) {
	i := strings.LastIndex(s, "/")
	if i < 0 {
		return mail.MessagePath{}, fmt.Errorf("bad attached message %q", s)
	}
	var p mail.MessagePath
	var err error
	p.Mailbox, p.UID, err = mail.ParseMessageRef(s[:i], s[i+1:])
	return p, err
}

// SelectedFrom is the From option the form opens on; only an exact "account" or "account|identity" value selects one.
func (d *ComposeRenderData) SelectedFrom() string {
	fallback := d.GlobalData.Username
	if d.GlobalData.URLAccount != "" {
		fallback = d.GlobalData.URLAccount
	}
	want := mail.BareAddress(d.Message.From)
	if want == "" {
		return fallback
	}
	for _, group := range d.Identities {
		if strings.EqualFold(want, group.Account) {
			return group.Account
		}
		for _, identity := range group.Addresses {
			if strings.EqualFold(want, mail.BareAddress(identity)) {
				return group.Account + "|" + identity
			}
		}
	}
	return fallback
}

// Uploads are the files the form had taken before it was sent back.
func (d *ComposeRenderData) Uploads() []*mail.FormAttachment {
	var out []*mail.FormAttachment
	for _, att := range d.Message.Attachments {
		if a, ok := att.(*mail.FormAttachment); ok && a.ID != "" {
			out = append(out, a)
		}
	}
	return out
}

func (d *ComposeRenderData) PartAttachments() []*mail.IMAPAttachment {
	var out []*mail.IMAPAttachment
	for _, att := range d.Message.Attachments {
		if a, ok := att.(*mail.IMAPAttachment); ok {
			out = append(out, a)
		}
	}
	return out
}

func attachedList(msg *mail.OutgoingMessage) []AttachedMessage {
	var out []AttachedMessage
	for _, att := range msg.Attachments {
		if m, ok := att.(*mail.MessageAttachment); ok {
			out = append(out, AttachedMessage{Ref: attachedRef(m.Path), Name: m.Name})
		}
	}
	return out
}

func attachedRef(p mail.MessagePath) string {
	return fmt.Sprintf("%s/%v", url.PathEscape(p.Mailbox), p.UID)
}

// The form shows only names; the send fetches the messages.

// The sender is the From account; the draft and the answered message stay on the request's account.
func (p *plugin) submitCompose(ctx *web.Context, sender *alborz.Login, msg *mail.OutgoingMessage, options *mail.ComposeOptions) error {
	sent, sentUID, to, err := p.Submit(ctx.Request().Context(), ctx.Login, sender, msg, options)
	if _, ok := err.(alborz.AuthError); ok {
		return web.NewError(http.StatusForbidden, err)
	}
	if err != nil {
		return err
	}
	// The message has left: what a feature does with that is not the send's to fail.
	for _, sent := range ctx.Server.OnSent {
		if err := sent(ctx, sender, to); err != nil {
			ctx.Logger().Printf("after sending: %v", err)
		}
	}

	notice := web.Notice{Kind: web.NoticeDone, Text: ctx.T("notice.sent")}
	if sent != nil && sentUID != 0 {
		// The Sent copy may be outside the reader's scope, so the link names its account.
		view := ctx.Path("mail.message", sent.Name(), sentUID) + "?account=" + web.QueryValue(sender.Username())
		notice.Actions = []*web.NoticeAction{{Label: ctx.T("notice.view"), Path: view}}
	}
	ctx.Notify(notice)
	return ctx.Redirect(http.StatusFound, ctx.URL("mail.mailbox", "INBOX"))
}

func (p *plugin) handleCompose(ctx *web.Context, msg *mail.OutgoingMessage, options *mail.ComposeOptions) error {
	ibase, err := p.newIMAPBaseRenderData(ctx, web.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	msg.Mailer = mail.Mailer()

	if msg.From == "" && strings.ContainsRune(ctx.Login.Username(), '@') {
		msg.From = mail.FromAddress(settings, ctx.Login.Username())
	}

	if ctx.Request().Method == http.MethodPost {
		formParams, err := ctx.FormParams()
		if err != nil {
			return fmt.Errorf("failed to parse form: %v", err)
		}
		_, saveAsDraft := formParams["save_as_draft"]

		// SMTP, Sent and settings follow the From account; the draft, the answered message and attachments stay on the request's.
		sender := ctx.Login
		account, identity := mail.SplitIdentityChoice(ctx.FormValue("from_account"))
		if account != "" && account != sender.Username() {
			sender = ctx.LoginFor(account)
			if sender == nil {
				return web.NewError(http.StatusBadRequest, "not signed in to that account")
			}
		}
		settings, err := mail.LoadSettings(ctx.Request().Context(), sender.Store())
		if err != nil {
			return err
		}
		render := func(status int, signature, errText string) error {
			ibase.WithTitle(ctx.T("aside.compose"))
			ibase.Refused(errText)
			return ctx.Render(status, "mail/compose", &ComposeRenderData{
				IMAPBaseRenderData: *ibase,
				Message:            msg,
				Attached:           attachedList(msg),
				Identities:         composeIdentities(ctx, msg.From),
				Signatures:         settings.Signatures,
				Signature:          signature,
				Suggestions:        suggestions(ctx),
				Reply:              options.InReplyTo != nil,
				LocalImage:         p.images.String(),
			})
		}
		// A server's refusal comes back on the form; uploads stay with the session until the message has left.
		var uploads []string
		submit := func(f func() error) error {
			err := f()
			var no mail.Refused
			if errors.As(err, &no) {
				return render(http.StatusUnprocessableEntity, ctx.FormValue("signature"), fmt.Sprintf(ctx.T(no.Key), no.Err))
			}
			if err == nil {
				for _, id := range uploads {
					if a := ctx.PopAttachment(id); a != nil {
						a.Close()
					}
				}
			}
			return err
		}
		msg.From = mail.FromAddress(settings, sender.Username())
		// No Sender header: an identity is the same person, not a different transmitter (RFC 5322 3.6.2).
		if identity != "" && slices.Contains(settings.Identities, identity) {
			msg.From = identity
		}
		for _, field := range []struct {
			name string
			into *[]string
		}{{"to", &msg.To}, {"cc", &msg.Cc}, {"bcc", &msg.Bcc}, {"reply_to", &msg.ReplyTo}} {
			addresses, err := mail.ParseAddressList(ctx.FormValue(field.name))
			if err != nil {
				return render(http.StatusUnprocessableEntity, ctx.FormValue("signature"), ctx.T("form.recipientinvalid"))
			}
			*field.into = addresses
		}
		msg.Subject = ctx.FormValue("subject")
		msg.Text = ctx.FormValue("text")
		// The text is derived from the HTML so the plain part and the draft agree.
		if msg.HTML = mail.ComposedHTML(p.composed, ctx.FormValue("html")); msg.HTML != "" {
			msg.Text = mail.ComposedText(msg.HTML)
		}

		// Choosing a signature is a submit, so it works without script.
		if _, ok := formParams["signature_apply"]; ok {
			chosen := ctx.FormValue("signature")
			sig, _ := settings.SignatureNamed(chosen)
			msg.Text = mail.WithSignature(msg.Text, sig.Text)
			if msg.HTML != "" {
				msg.HTML = mail.WithSignatureHTML(msg.HTML, sig.Text)
			}
			return render(http.StatusOK, sig.Name, "")
		}
		// Read at send, never from a page flag, which a tab opened earlier can lose.
		msg.SendHTML = msg.HTML == "" && !ctx.Profile().Writing.NoDirectionPart && lang.HasRTL(msg.Text)
		if msg.SendHTML && options.InReplyTo != nil {
			toList := false
			if id, err := mail.MessageListID(ctx.Request().Context(), ctx.Login, *options.InReplyTo); err != nil {
				// Unknown counts as a list: plain text to a person is the smaller harm.
				ctx.Logger().Printf("list check for reply: %v", err)
				toList = true
			} else {
				toList = id != ""
			}
			msg.SendHTML = !toList
		}
		msg.InReplyTo = ctx.FormValue("in_reply_to")
		msg.MessageID = ctx.FormValue("message_id")
		if msg.MessageID == "" {
			// The page carries the id so a draft saved twice stays one message.
			msg.MessageID = mail.NewMessageID()
		}

		form, err := ctx.MultipartForm()
		if err != nil {
			return fmt.Errorf("failed to get multipart form: %v", err)
		}

		var attached []mail.MessagePath
		for _, s := range form.Value[attachedField] {
			p, err := parseAttachedRef(s)
			if err != nil {
				return web.NewError(http.StatusBadRequest, err)
			}
			attached = append(attached, p)
		}
		if err := mail.AttachMessages(ctx.Request().Context(), ctx.Login, msg, attached); err != nil {
			return err
		}
		options.Attached = attached

		var original *mail.MessagePath
		if options.Draft != nil {
			original = options.Draft
		} else if options.Forward != nil {
			original = options.Forward
		}
		if original != nil {
			for _, s := range form.Value["prev_attachments"] {
				path, err := mail.ParsePartPath(s)
				if err != nil {
					return fmt.Errorf("failed to parse original attachment path: %v", err)
				}

				if err := mail.AttachPart(ctx.Request().Context(), ctx.Login, msg, *original, path); err != nil {
					return err
				}
			}
		} else if len(form.Value["prev_attachments"]) > 0 {
			return fmt.Errorf("previous attachments specified but no original message available")
		}

		for _, f := range form.File["attachments"] {
			msg.Attachments = append(msg.Attachments, formAttachment(f, ""))
		}

		uuids := ctx.FormValue("attachment-uuids")
		for _, uuid := range strings.Split(uuids, ",") {
			if uuid == "" {
				continue
			}

			attachment := ctx.PeekAttachment(uuid)
			if attachment == nil {
				return fmt.Errorf("unable to retrieve message attachment %s from session", uuid)
			}
			msg.Attachments = append(msg.Attachments, formAttachment(attachment, uuid))
			uploads = append(uploads, uuid)
		}

		if msg.HTML != "" {
			images, err := mail.InlineImages(msg.HTML, msg.MessageID, imageSource(ctx, &uploads))
			if err != nil {
				return err
			}
			msg.Inline = images
		}

		// A draft may be addressed later; a message with nowhere to go is not sent.
		if !saveAsDraft && len(msg.To) == 0 && len(msg.Cc) == 0 && len(msg.Bcc) == 0 {
			return render(http.StatusUnprocessableEntity, ctx.FormValue("signature"), ctx.T("form.recipientneeded"))
		}

		return submit(func() error {
			if saveAsDraft {
				drafts, uid, err := p.SaveDraft(ctx.Request().Context(), ctx.Login, msg, options.Draft)
				if err != nil {
					return err
				}
				ctx.PutNotice(ctx.T("notice.draftsaved"))
				return ctx.Redirect(http.StatusFound, ctx.Path("mail.message.edit", drafts.Mailbox, uid)+"?part=1")
			}
			return p.submitCompose(ctx, sender, msg, options)
		})
	}

	ibase.WithTitle(ctx.T("aside.compose"))
	data := &ComposeRenderData{
		Identities:         composeIdentities(ctx, msg.From),
		IMAPBaseRenderData: *ibase,
		Message:            msg,
		Attached:           attachedList(msg),
		Signatures:         settings.Signatures,
		Signature:          options.Signature,
		Suggestions:        suggestions(ctx),
		Reply:              options.InReplyTo != nil,
		LocalImage:         p.images.String(),
	}
	return ctx.Render(http.StatusOK, "mail/compose", data)
}

// suggestions are what the features offer, each entry once, sorted.
func suggestions(ctx *web.Context) []string {
	var all []string
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

func (p *plugin) handleComposeNew(ctx *web.Context) error {
	// Only a mailto URI is followed; redirecting to anything else would be an open redirect.
	if uri := ctx.QueryParam("mailto"); uri != "" {
		q := mail.Mailto(uri)
		if q == nil {
			return web.NewError(http.StatusBadRequest, "not a mailto URI")
		}
		return ctx.Redirect(http.StatusFound, ctx.Path("mail.compose")+"?"+web.Query(q))
	}

	text := ctx.QueryParam("body")
	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	chosen := settings.DefaultSignature
	if sig, ok := settings.SignatureNamed(chosen); ok && text == "" {
		text = mail.WithSignature(text, sig.Text)
	} else if !ok {
		chosen = ""
	}

	return p.handleCompose(ctx, &mail.OutgoingMessage{
		From:        p.OwnIdentity(ctx.Login, settings, ctx.QueryParam("from")),
		To:          strings.Split(ctx.QueryParam("to"), ","),
		Subject:     ctx.QueryParam("subject"),
		MessageID:   mail.NewMessageID(),
		InReplyTo:   ctx.QueryParam("in-reply-to"),
		Text:        text,
		Attachments: uploaded(ctx),
	}, &mail.ComposeOptions{Signature: chosen})
}

// composeTooLarge answers a message form past its limit; what was typed did not arrive.
func composeTooLarge(ctx *web.Context) error {
	return web.NewError(http.StatusRequestEntityTooLarge, ctx.T("attach.toolarge"))
}

func handleComposeAttachment(ctx *web.Context) error {
	reader, err := ctx.Request().MultipartReader()
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": ctx.T("attach.invalid")})
	}
	form, err := web.ReadForm(reader)
	var over *http.MaxBytesError
	if errors.As(err, &over) {
		return ctx.JSON(http.StatusRequestEntityTooLarge, map[string]string{"error": ctx.T("attach.toolarge")})
	} else if err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": ctx.T("attach.invalid")})
	}
	// The session holds the uploads it takes; the rest go with the request.
	files := form.File["attachments"]
	delete(form.File, "attachments")
	defer form.Close()

	// Each upload's addresses go with it, so the page names no path of its own.
	type upload struct {
		ID     string `json:"id"`
		Src    string `json:"src"`
		Remove string `json:"remove"`
	}
	var uploads []upload
	for i, f := range files {
		uuid, err := ctx.PutAttachment(f)
		if err != nil {
			for _, f := range files[i:] {
				f.Close()
			}
		}
		if err == web.ErrAttachmentCacheSize {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": ctx.T("attach.toolarge")})
		} else if err != nil {
			ctx.Logger().Printf("PutAttachment: %v\n", err)
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": ctx.T("attach.failed")})
		}
		uploads = append(uploads, upload{ID: uuid, Src: ctx.Path("mail.attachment", uuid), Remove: ctx.Path("mail.attachment.remove", uuid)})
	}

	return ctx.JSON(http.StatusOK, &uploads)
}

func formAttachment(f *web.File, id string) *mail.FormAttachment {
	return &mail.FormAttachment{Name: f.Filename, ContentType: f.Header.Get("Content-Type"), Body: f.Spool, ID: id}
}

func handleCancelAttachment(ctx *web.Context) error {
	uuid := ctx.Param("uuid")
	a := ctx.PopAttachment(uuid)
	if a != nil {
		a.Close()
	}
	return ctx.JSON(http.StatusOK, nil)
}

func (p *plugin) handleReply(ctx *web.Context) error {
	var inReplyToPath mail.MessagePath
	var err error
	inReplyToPath.Mailbox, inReplyToPath.UID, err = MessageRef(ctx)
	if err != nil {
		return err
	}

	var msg mail.OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		msg, err = p.populateMessageFromOriginalMessage(ctx, inReplyToPath)
		if err != nil {
			return err
		}
	}

	return p.handleCompose(ctx, &msg, &mail.ComposeOptions{InReplyTo: &inReplyToPath})
}

// A bare link names no part, so the text part shown is fetched instead.
func quotablePart(ctx *web.Context, path mail.MessagePath, partPath []int) (*mail.IMAPMessage, *message.Entity, string, error) {
	msg, part, mimeType, err := mail.Quotable(ctx.Request().Context(), ctx.Login, path, partPath)
	if errors.Is(err, mail.ErrNoText) {
		return nil, nil, "", web.NewError(http.StatusBadRequest, err)
	}
	return msg, part, mimeType, err
}

func (p *plugin) populateMessageFromOriginalMessage(ctx *web.Context, inReplyToPath mail.MessagePath) (mail.OutgoingMessage, error) {
	var ret mail.OutgoingMessage

	partPath, err := mail.ParsePartPath(ctx.QueryParam("part"))
	if err != nil {
		return ret, web.NewError(http.StatusBadRequest, err)
	}

	inReplyTo, part, mimeType, err := quotablePart(ctx, inReplyToPath, partPath)
	if err != nil {
		return ret, err
	}

	var quoted string
	switch mimeType {
	case "text/plain":
		var text string
		if text, err = mail.ReadText(part); err != nil {
			return ret, err
		}
		quoted, err = mail.Quote(strings.NewReader(text))
	case "text/html":
		var text string
		text, err = html2text.FromReader(part.Body, html2text.Options{})
		if err != nil {
			return ret, err
		}

		quoted, err = mail.Quote(strings.NewReader(text))
	default:
		defErr := fmt.Errorf("cannot forward %q part", mimeType)
		err = web.NewError(http.StatusBadRequest, defErr)
	}
	if err != nil {
		return ret, err
	}

	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return ret, err
	}
	below := ctx.Profile().Writing.ReplyBelowQuote
	ret.Text = replyBody(ctx, inReplyTo, quoted, below)
	ret.QuoteBelow = !below
	if quoted, err := p.quotedHTML(ctx, inReplyToPath, inReplyTo); err != nil {
		return ret, err
	} else if quoted != "" {
		ret.QuoteHTML = replyHTML(ctx, inReplyTo, quoted, below)
		ret.QuoteRich = true
	}

	ret.MessageID = mail.NewMessageID()
	ret.InReplyTo = "<" + inReplyTo.Envelope.MessageID + ">"
	ret.References = strings.TrimSpace(inReplyTo.References + " " + ret.InReplyTo)
	// A mail to an identity is answered by that identity; Cc counts too.
	ret.From = p.ReplyIdentity(ctx.Login, settings, inReplyTo)
	replyTo := inReplyTo.Envelope.ReplyTo
	if len(replyTo) == 0 {
		replyTo = inReplyTo.Envelope.From
	}
	ret.To = mail.AddressStrings(replyTo)

	if ctx.QueryParam("list") != "" && inReplyTo.ListPost != "" {
		ret.To = []string{inReplyTo.ListPost}
	} else if ctx.QueryParam("all") != "" {
		filtered := mail.WithoutAccount(ctx.Login.Username(),
			inReplyTo.Envelope.To)
		ret.To = mail.AddressStrings(append(replyTo, filtered...))
		ret.Cc = mail.AddressStrings(inReplyTo.Envelope.Cc)
	}

	ret.Subject = inReplyTo.Envelope.Subject
	if !strings.HasPrefix(strings.ToLower(ret.Subject), "re:") {
		ret.Subject = "Re: " + ret.Subject
	}

	return ret, nil
}

// Not quoted: chevrons on every line are reply convention, not forward.
func forwardBody(ctx *web.Context, original *mail.IMAPMessage, body string) string {
	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString(ctx.T("message.forwardedblock"))
	b.WriteString("\n")
	if from := original.Envelope.From; len(from) > 0 {
		fmt.Fprintf(&b, "From: %s\n", addressLine(from[0]))
	}
	fmt.Fprintf(&b, "Date: %s\n", ctx.Dates().Full(original.Date()))
	fmt.Fprintf(&b, "Subject: %s\n", original.Envelope.Subject)
	if to := original.Envelope.To; len(to) > 0 {
		fmt.Fprintf(&b, "To: %s\n", strings.Join(mail.AddressStrings(to), ", "))
	}
	b.WriteString("\n")
	b.WriteString(body)
	return b.String()
}

func forwardHTML(ctx *web.Context, original *mail.IMAPMessage, body string) template.HTML {
	head := strings.TrimSpace(forwardBody(ctx, original, ""))
	lines := strings.Split(html.EscapeString(head), "\n")
	return template.HTML(`<p dir="auto"><br></p>` + "\n" +
		`<p dir="` + lang.ParagraphDir(head) + `">` + strings.Join(lines, "<br>") + "</p>\n<div>" + body + "</div>\n")
}

func (p *plugin) quotedHTML(ctx *web.Context, path mail.MessagePath, msg *mail.IMAPMessage) (string, error) {
	raw := ctx.Path("mail.message.raw", msg.Mailbox, msg.UID)
	quoted, err := mail.QuotedHTML(ctx.Request().Context(), ctx.Login, path, msg, func(part *mail.IMAPPartNode) string {
		return raw + partQuery(*part, ctx.Login.Username())
	})
	if err != nil || quoted == "" {
		return "", err
	}
	return mail.ComposedHTML(p.composed, quoted), nil
}

func addressLine(a imap.Address) string {
	if a.Name != "" {
		return fmt.Sprintf("%s <%s>", a.Name, a.Addr())
	}
	return a.Addr()
}

func replyBody(ctx *web.Context, original *mail.IMAPMessage, quoted string, below bool) string {
	who := ""
	if from := original.Envelope.From; len(from) > 0 {
		who = from[0].Name
		if who == "" {
			who = from[0].Addr()
		}
	}
	when := ctx.Dates().Full(original.Date())
	attribution := ctx.Tf("message.wrote", when, who)
	if below {
		return attribution + "\n" + quoted + "\n"
	}
	return "\n\n" + attribution + "\n" + quoted
}

func replyHTML(ctx *web.Context, original *mail.IMAPMessage, quoted string, below bool) template.HTML {
	who := ""
	if from := original.Envelope.From; len(from) > 0 {
		who = from[0].Name
		if who == "" {
			who = from[0].Addr()
		}
	}
	when := ctx.Dates().Full(original.Date())
	attribution := ctx.Tf("message.wrote", when, who)
	head := `<p dir="` + lang.ParagraphDir(attribution) + `">` + html.EscapeString(attribution) + "</p>\n<blockquote>" + quoted + "</blockquote>\n"
	if below {
		return template.HTML(head + `<p dir="auto"><br></p>`)
	}
	return template.HTML(`<p dir="auto"><br></p>` + "\n" + head)
}

func handleForwardSelection(ctx *web.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	params, err := ctx.FormParams()
	if err != nil {
		return web.NewError(http.StatusBadRequest, err)
	}
	uids, err := mail.ParseUIDs(params["uids"])
	if err != nil {
		return web.NewError(http.StatusBadRequest, err)
	}
	back := ctx.NextOr(mailboxURL(ctx, mboxName))
	if len(uids) == 0 {
		return nothingSelected(ctx, back)
	}
	// One message is forwarded inline where it can be trimmed; several travel whole as attachments.
	if len(uids) == 1 {
		return ctx.Redirect(http.StatusFound, ctx.URL("mail.message.forward", mboxName, uids[0]))
	}

	refs := make([]string, len(uids))
	for i, uid := range uids {
		refs[i] = fmt.Sprintf("%v", uid)
	}
	q := url.Values{"uids": {strings.Join(refs, ",")}}
	if a := ctx.URLAccount(); a != "" {
		q.Set("account", web.QueryValue(a))
	}
	return ctx.Redirect(http.StatusFound, ctx.Path("mail.messages.forward", mboxName)+"?"+web.Query(q))
}

// A GET so the page can be reloaded; the selection arrives in the query.
func (p *plugin) handleForwardAttached(ctx *web.Context) error {
	mboxName, err := mailboxRef(ctx)
	if err != nil {
		return err
	}
	uids, err := mail.ParseUIDs(strings.Split(ctx.QueryParam("uids"), ","))
	if err != nil || len(uids) == 0 {
		return web.NewError(http.StatusBadRequest, "no messages to forward")
	}
	paths := make([]mail.MessagePath, len(uids))
	for i, uid := range uids {
		paths[i] = mail.MessagePath{Mailbox: mboxName, UID: uid}
	}
	var msg mail.OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		if err := mail.AttachMessages(ctx.Request().Context(), ctx.Login, &msg, paths); err != nil {
			return err
		}
		msg.MessageID = mail.NewMessageID()
		msg.Subject = ctx.Tf("message.forwardcount", len(uids))
		msg.QuoteBelow = true
	}
	return p.handleCompose(ctx, &msg, &mail.ComposeOptions{Attached: paths})
}

func (p *plugin) handleForward(ctx *web.Context) error {
	var sourcePath mail.MessagePath
	var err error
	sourcePath.Mailbox, sourcePath.UID, err = MessageRef(ctx)
	if err != nil {
		return err
	}

	var msg mail.OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		partPath, err := mail.ParsePartPath(ctx.QueryParam("part"))
		if err != nil {
			return web.NewError(http.StatusBadRequest, err)
		}

		source, part, mimeType, err := quotablePart(ctx, sourcePath, partPath)
		if err != nil {
			return err
		}

		var body string
		switch mimeType {
		case "text/plain":
			body, err = mail.ReadText(part)
		case "text/html":
			// Lossy; attaching the message instead keeps it whole.
			body, err = html2text.FromReader(part.Body, html2text.Options{})
		default:
			defErr := fmt.Errorf("cannot forward %q part", mimeType)
			err = web.NewError(http.StatusBadRequest, defErr)
		}
		if err != nil {
			return err
		}
		msg.Text = forwardBody(ctx, source, body)
		msg.QuoteBelow = true
		if quoted, err := p.quotedHTML(ctx, sourcePath, source); err != nil {
			return err
		} else if quoted != "" {
			msg.QuoteHTML = forwardHTML(ctx, source, quoted)
			msg.QuoteRich = true
		}

		msg.MessageID = mail.NewMessageID()
		msg.Subject = source.Envelope.Subject
		if !strings.HasPrefix(strings.ToLower(msg.Subject), "fwd:") &&
			!strings.HasPrefix(strings.ToLower(msg.Subject), "fw:") {
			msg.Subject = "Fwd: " + msg.Subject
		}
		// A forward starts a thread of its own rather than joining the original's.
		msg.References = "<" + source.Envelope.MessageID + ">"

		attachments := source.Attachments()
		for i := range attachments {
			msg.Attachments = append(msg.Attachments, &mail.IMAPAttachment{
				Mailbox: sourcePath.Mailbox,
				UID:     sourcePath.UID,
				Node:    &attachments[i],
			})
		}
	}

	return p.handleCompose(ctx, &msg, &mail.ComposeOptions{Forward: &sourcePath})
}

func (p *plugin) handleEdit(ctx *web.Context) error {
	var sourcePath mail.MessagePath
	var err error
	sourcePath.Mailbox, sourcePath.UID, err = MessageRef(ctx)
	if err != nil {
		return err
	}

	// TODO: somehow get the path to the In-Reply-To message (with a search?)

	var msg mail.OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		partPath, err := mail.ParsePartPath(ctx.QueryParam("part"))
		if err != nil {
			return web.NewError(http.StatusBadRequest, err)
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
			return web.NewError(http.StatusBadRequest, err)
		}

		if msg.Text, err = mail.ReadText(part); err != nil {
			return fmt.Errorf("failed to read part body: %v", err)
		}
		if msg.HTML, err = p.quotedHTML(ctx, sourcePath, source); err != nil {
			return fmt.Errorf("failed to read the draft's HTML: %v", err)
		}

		if len(source.Envelope.From) > 0 {
			msg.From = source.Envelope.From[0].Addr()
		}
		msg.To = mail.AddressStrings(source.Envelope.To)
		msg.Subject = source.Envelope.Subject
		msg.InReplyTo = formatMsgIDList(source.Envelope.InReplyTo)
		msg.MessageID = "<" + source.Envelope.MessageID + ">"

		attachments := source.Attachments()
		for i := range attachments {
			msg.Attachments = append(msg.Attachments, &mail.IMAPAttachment{
				Mailbox: sourcePath.Mailbox,
				UID:     sourcePath.UID,
				Node:    &attachments[i],
			})
		}
	}

	return p.handleCompose(ctx, &msg, &mail.ComposeOptions{Draft: &sourcePath})
}

func formatMsgIDList(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return "<" + strings.Join(l, ">, <") + ">"
}
