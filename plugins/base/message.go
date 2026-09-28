package alborzbase

import (
	"archive/zip"
	"bufio"
	"bytes"
	"fmt"
	"html"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"
	"github.com/emersion/go-smtp"
	"github.com/labstack/echo/v4"
	"jaytaylor.com/html2text"
)

type MessageRenderData struct {
	IMAPBaseRenderData
	Message *IMAPMessage
	Part    *IMAPPartNode
	// AsText is the HTML part read as words, offered only without a plain part.
	AsText         bool
	View           interface{}
	MailboxPage    int
	Flags          map[imap.Flag]bool
	ContextPending bool
	ContextURL     template.URL

	// Invitation is the scheduling request this message carries, if any.
	Invitation *Invitation

	// AuthResults is what the receiving server said about the sender's domain.
	AuthResults *AuthResults
	// Warnings are the indicators that earned a colour, and Mark the colour.
	Warnings []Indicator
	Mark     Grade

	// Unsubscribe is where the unsubscribe control sends the reader.
	Unsubscribe string

	// InReplyTo is the message this one answers, when it is in the same folder.
	InReplyTo *ThreadNeighbour

	// Answers are the messages answering this one, found only in Sent.
	Answers []ThreadNeighbour

	// ThreadSupported says the server can group the folder into conversations.
	ThreadSupported bool

	// Crumb is the path to the folder this message is in, account first.
	Crumb []CrumbLink
	// PreferHTML is the account's choice of which part the tabs open.
	PreferHTML bool

	// DeliveredTo names the alias the message was delivered to, if not the account itself.
	DeliveredTo string

	// ForwardedBy names the mailbox that passed the message on, proved by our server's SPF check.
	ForwardedBy string

	// Neighbors in the view the message was opened from; nil when absent.
	NewerURL *url.URL
	OlderURL *url.URL
	Position int
	Total    int
	Query    string

	// Signature is what the page says about the message's authenticity; its zero value says nothing.
	Signature Verification

	// HasRemoteResources says the shown part asks for resources elsewhere, loaded only when RemoteResourcesAllowed.
	HasRemoteResources     bool
	RemoteResourcesAllowed bool

	// Filters offers the rule shortcuts, where an account has a filter server.
	Filters bool

	// Filled by calendars and contacts, nil where they have nothing: the share this mail announces,
	// where an invitation or an attached file can be filed, and the kept copy a withdrawal can drop.
	ShareOffer      any
	InviteCalendars any
	ImportCalendars any
	ImportBooks     any
	InviteFiled     string
}

// Enough for mail with a few files, while mail with fifty keeps the body in view.
const attachmentsShown = 5

// AttachmentsOpen reports whether the card starts open.
func (d *MessageRenderData) AttachmentsOpen() bool {
	return len(d.Message.Attachments()) <= attachmentsShown
}

// The invitation is re-read from the message, so what is answered is what was sent.
func handleInvitationReply(ctx *alborz.Context) error {
	mboxName, uid, err := messageRef(ctx)
	if err != nil {
		return err
	}

	status := strings.ToUpper(ctx.FormValue("status"))
	switch status {
	case partAccepted, partDeclined, partTentative:
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unknown answer")
	}

	var msg *IMAPMessage
	if err := ctx.DoIMAP(func(c *imapclient.Client) error {
		var err error
		msg, _, err = getMessagePart(c, mboxName, uid, nil)
		return err
	}); err != nil {
		return err
	}
	inv := messageInvitation(ctx, msg, mboxName, uid)
	if inv == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "this message carries no invitation")
	}

	reply, err := invitationReply(ctx, inv, status)
	if err != nil {
		return err
	}
	reply.Mailer = mailerName(ctx)
	back := ctx.NextOr(ctx.AccountPath(fmt.Sprintf("/message/%s/%v", url.PathEscape(mboxName), uid)))
	if err := ctx.DoSMTP(func(c *smtp.Client) error {
		return sendMessage(c, reply)
	}); err != nil {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.sendrefused"), err)})
		return ctx.Redirect(http.StatusFound, back)
	}

	ctx.PutNotice(ctx.T("invite.answered"))
	return ctx.Redirect(http.StatusFound, back)
}

// With plain the bytes are shown rather than saved: the source view.
func handleDownloadMessage(ctx *alborz.Context, plain bool) error {
	mboxName, uid, err := messageRef(ctx)
	if err != nil {
		return err
	}

	var raw []byte
	var env *imap.Envelope
	if err := ctx.DoIMAP(func(c *imapclient.Client) error {
		var err error
		raw, env, err = fetchRawMessage(c, mboxName, uid)
		return err
	}); err != nil {
		return err
	}
	if plain {
		// No charset can be right for a whole message whose parts each name their own.
		return ctx.Blob(http.StatusOK, "text/plain; charset=utf-8", raw)
	}

	subject := ""
	if env != nil {
		subject = env.Subject
	}
	ctx.Response().Header().Set("Content-Disposition",
		downloadName(subject, fmt.Sprintf("%v", uid), ".eml"))
	return ctx.Blob(http.StatusOK, "message/rfc822", raw)
}

func handleDownloadAttachments(ctx *alborz.Context) error {
	mboxName, uid, err := messageRef(ctx)
	if err != nil {
		return err
	}
	var raw []byte
	var env *imap.Envelope
	if err := ctx.DoIMAP(func(c *imapclient.Client) error {
		var err error
		raw, env, err = fetchRawMessage(c, mboxName, uid)
		return err
	}); err != nil {
		return err
	}
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) {
		return err
	}
	subject := ""
	if env != nil {
		subject = env.Subject
	}
	ctx.Response().Header().Set("Content-Disposition",
		downloadName(subject, fmt.Sprintf("%v", uid), ".zip"))
	ctx.Response().Header().Set("Content-Type", "application/zip")
	ctx.Response().WriteHeader(http.StatusOK)
	archive := zip.NewWriter(ctx.Response())
	taken := map[string]int{}
	if err := writeAttachments(archive, entity, taken, ""); err != nil {
		// The reader has bytes by now, so the file ends short rather than turning into an error page.
		ctx.Logger().Printf("attachments %q uid %v: %v", mboxName, uid, err)
	}
	return archive.Close()
}

func htmlAsText(part *message.Entity) (*message.Entity, error) {
	text, err := html2text.FromReader(part.Body, html2text.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to read the HTML as text: %v", err)
	}
	var header textproto.Header
	header.Set("Content-Type", "text/plain; charset=utf-8")
	return message.New(message.Header{Header: header}, strings.NewReader(text))
}

func joinedHTML(c *imapclient.Client, mboxName string, uid imap.UID, pieces []IMAPPartNode) (*message.Entity, error) {
	if err := ensureMailboxSelected(c, mboxName); err != nil {
		return nil, err
	}
	options := &imap.FetchOptions{}
	for _, p := range pieces {
		if p.MIMEType == "text/html" {
			options.BodySection = append(options.BodySection,
				&imap.FetchItemBodySection{Peek: true, Part: p.Path, Specifier: imap.PartSpecifierMIME},
				&imap.FetchItemBodySection{Peek: true, Part: p.Path})
		}
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uid), options).Collect()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch the HTML pieces: %v", err)
	} else if len(msgs) == 0 {
		return nil, alborz.NotFound("notfound.message", fmt.Sprint(uid))
	}
	var joined bytes.Buffer
	sections := options.BodySection
	for _, p := range pieces {
		if p.MIMEType != "text/html" {
			fmt.Fprintf(&joined, `<p><img src="part:%s" alt="%s"></p>`, p.PathString(), html.EscapeString(p.Filename))
			continue
		}
		headerBuf, bodyBuf := msgs[0].FindBodySection(sections[0]), msgs[0].FindBodySection(sections[1])
		sections = sections[2:]
		if headerBuf == nil || bodyBuf == nil {
			return nil, fmt.Errorf("server didn't return HTML piece %v", p.PathString())
		}
		h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(headerBuf)))
		if err != nil {
			return nil, err
		}
		piece, err := message.New(message.Header{Header: h}, bytes.NewReader(bodyBuf))
		if err != nil && !message.IsUnknownCharset(err) {
			return nil, err
		}
		if _, err := io.Copy(&joined, piece.Body); err != nil {
			return nil, err
		}
	}
	var h message.Header
	h.SetContentType("text/html", map[string]string{"charset": "utf-8"})
	return message.New(h, &joined)
}

func writeAttachments(archive *zip.Writer, e *message.Entity, taken map[string]int, parent string) error {
	mediaType, typeParams, _ := e.Header.ContentType()
	if mr := e.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil && !message.IsUnknownCharset(err) {
				return err
			}
			if err := writeAttachments(archive, part, taken, strings.TrimPrefix(mediaType, "multipart/")); err != nil {
				return err
			}
		}
	}
	disposition, params, _ := e.Header.ContentDisposition()
	filename := params["filename"]
	if filename == "" {
		filename = typeParams["name"]
	}
	if !attached(mediaType, disposition, filename, e.Header.Get("Content-Id"), parent) {
		return nil
	}
	if filename == "" && strings.EqualFold(mediaType, "message/rfc822") {
		body, err := io.ReadAll(e.Body)
		if err != nil {
			return err
		}
		inner, err := mail.CreateReader(bytes.NewReader(body))
		if err == nil {
			subject, _ := inner.Header.Subject()
			filename = attachmentName(subject)
		}
		e.Body = bytes.NewReader(body)
	}
	w, err := archive.Create(zipEntryName(filename, mediaType, taken))
	if err != nil {
		return err
	}
	_, err = io.Copy(w, e.Body)
	return err
}

func zipEntryName(filename, mediaType string, taken map[string]int) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == '\\', r == '/', r == 0x7f:
			return -1
		}
		return r
	}, filename)
	if name == "" {
		name = "part"
		if exts, err := mime.ExtensionsByType(mediaType); err == nil && len(exts) > 0 {
			name += exts[0]
		}
	}
	taken[name]++
	if n := taken[name]; n > 1 {
		ext := path.Ext(name)
		name = fmt.Sprintf("%s %d%s", strings.TrimSuffix(name, ext), n, ext)
	}
	return name
}

func (p *plugin) handleGetPart(ctx *alborz.Context, raw bool) error {
	if !raw && ctx.PartialFor("message-navigation") {
		return p.handleMessageNavigation(ctx)
	}
	if raw && ctx.QueryParam("part") == "" {
		return handleDownloadMessage(ctx, ctx.QueryParam("plain") == "1")
	}
	mboxName, uid, err := messageRef(ctx)
	if err != nil {
		return err
	}

	partPath, err := parsePartPath(ctx.QueryParam("part"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	// Read before the message's IMAP work: it may sample the inbox, and the login lock is not reentrant.
	trusted := p.TrustedAuthServ(ctx, settings)
	messagesPerPage := PerPage(ctx)

	query := ctx.QueryParam("query")
	railView, err := readView(ctx)
	if err != nil {
		return err
	}

	var (
		cached *listingEntry
		place  listPlace
		placed bool
	)
	if !raw {
		cached, place, placed = p.cachedPlace(ctx, mboxName, uid, query, railView)
	}
	// A link naming no part opens the part the rows would link to; the cached row knows it without a fetch.
	if !raw && ctx.QueryParam("part") == "" && cached != nil {
		if row := cached.row(uid); row != nil {
			if preferred := row.PreferredPart(ctx.Reading().PreferHTML); preferred != nil && len(preferred.Path) > 0 {
				partPath = preferred.Path
			}
		}
	}
	// A body fetched ahead serves the page, unless signed: the check reads parts on the folder's connection.
	var held *cachedBody
	if !raw {
		held = p.bodies.current(ctx.Login.Username(), mboxName, uid, partPath)
		if held != nil && len(partPath) == 0 {
			partPath = held.part
		}
		if held != nil && held.buf.BodyStructure != nil {
			if isSigned(held.buf.BodyStructure) {
				held = nil
			}
		}
	}
	if !raw {
		alborz.CacheTiming(ctx.Request().Context(), "body", held != nil)
	}
	var (
		sb              sidebar
		msg             *IMAPMessage
		part            *message.Entity
		permanentFlags  []imap.Flag
		signature       Verification
		authResults     *AuthResults
		inReplyTo       *ThreadNeighbour
		answers         []ThreadNeighbour
		threadAlgorithm imap.ThreadAlgorithm
	)
	// A flag change evicts the listing but not the held body, so the sidebar may load below.
	if cached != nil {
		sb = cached.sb
	}
	if held != nil && cached == nil {
		sb, err = p.sidebarFor(ctx.Login)
		if err != nil {
			return err
		}
		sb = sb.clone()
		sb.active = sb.statuses[mboxName]
	}
	if held != nil {
		if msg, part, err = messagePart(held.buf, mboxName, uid, partPath); err != nil {
			return err
		}
		permanentFlags = held.permanent
		authResults = readAuthResults(messageRootHeader(msg), trusted)
		// The peek left it unread on the server; the page is what reads it.
		if !msg.HasFlag(imap.FlagSeen) {
			p.markHeldSeen(ctx.Login, mboxName, uid, held.validity)
		}
	}
	sent := sentFolder(sb.mailboxes)
	needsThread := !raw && msg != nil && (len(msg.Envelope.InReplyTo) > 0 || msg.References != "" || (sent != "" && mboxName == sent))
	deferContext := held != nil && sb.active != nil && ctx.Partial() && (!placed || needsThread)
	if cached != nil {
		threadAlgorithm = cached.threadAlgorithm
	}
	if held == nil || sb.active == nil || (!deferContext && (!placed || needsThread)) {
		err = ctx.DoIMAP(func(c *imapclient.Client) error {
			var load *sidebarLoad
			var err error
			if !raw && sb.active == nil {
				if load, err = startSidebar(c, mboxName, mboxName, settings.Subscriptions); err != nil {
					return err
				}
			}
			if held == nil {
				if msg, part, err = getMessagePart(c, mboxName, uid, partPath); err != nil {
					return err
				}
				// The cached listings are marked read rather than fetched again for one flag.
				p.messageRead(ctx.Login.Username(), mboxName, uid)
				permanentFlags = c.Mailbox().PermanentFlags
			}
			if raw {
				return nil
			}
			if !placed {
				if place, err = placeInList(ctx, c, settings, mboxName, uid, query, railView); err != nil {
					return err
				}
			}
			threadAlgorithm = ThreadAlgorithm(c)
			if load != nil {
				sent = sentFolder(load.sb.mailboxes)
			}
			inReplyTo, answers = readThread(ctx, c, mboxName, sent, msg)
			if held == nil && msg.BodyStructure != nil {
				signature = p.verifySignature(ctx.Request().Context(), ctx.Server.Outgoing.Remote(wkdTimeout), c, mboxName, uid, msg.BodyStructure,
					messageRootHeader(msg), envelopeSender(msg.Envelope))
				authResults = readAuthResults(messageRootHeader(msg), trusted)
				signature = withDelivery(signature, authResults)
			}
			if load != nil {
				sb, err = load.finish()
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	// Not in Junk: a message filed as junk needs no telling.
	var indicators []Indicator
	if !raw && folderRole(sb.mailboxes, mboxName) != "junk" {
		evidence := &Evidence{
			Header:     messageRootHeader(msg),
			Trusted:    trusted,
			Auth:       authResults,
			Subject:    msg.Envelope.Subject,
			From:       envelopeSender(msg.Envelope),
			MoneyWords: strings.Split(ctx.T("indicator.moneywords"), ","),
		}
		if evidence.From != "" {
			// Every signed-in account's inbox is asked: an imitation may reach a different address.
			for _, s := range ctx.Logins() {
				if book := p.senderBookFor(s); book != nil && evidence.Named == nil {
					evidence.Named = book.named(msg.Envelope.From[0].Name, evidence.From)
				}
			}
		}
		indicators = Indicators(evidence)
	}
	var warnings []Indicator
	mark := Mark(indicators, msg.NotJunk())
	if mark != Fact {
		warnings = Warnings(indicators)
	}

	// The bare envelope has no viewer.
	if !raw && len(partPath) == 0 {
		preferred := msg.PreferredPart(ctx.Reading().PreferHTML)
		if preferred != nil && len(preferred.Path) > 0 {
			q := ctx.Request().URL.Query()
			q.Set("part", preferred.PathString())
			return ctx.Redirect(http.StatusFound, msg.URL().String()+"?"+alborz.Query(q))
		}
	}

	if len(partPath) > 0 && msg.PartByPath(partPath) == nil {
		return echo.NewHTTPError(http.StatusNotFound,
			fmt.Sprintf("message %v has no part %v", uid, ctx.QueryParam("part")))
	}

	mimeType, _, err := part.Header.ContentType()
	if err != nil {
		return fmt.Errorf("failed to parse part Content-Type: %v", err)
	}

	if raw {
		ctx.Response().Header().Set("Content-Type", mimeType)

		disp, _, _ := part.Header.ContentDisposition()

		// Never serve text/html and the like inline.
		if !strings.EqualFold(mimeType, "text/plain") || strings.EqualFold(disp, "attachment") {
			var filename string
			if node := msg.PartByPath(partPath); node != nil {
				filename = node.Filename
			}
			disp := mime.FormatMediaType("attachment", map[string]string{
				"filename": zipEntryName(filename, mimeType, map[string]int{}),
			})
			ctx.Response().Header().Set("Content-Disposition", disp)
		}

		return ctx.Stream(http.StatusOK, mimeType, part.Body)
	}

	if pieces := msg.HTMLPieces(partPath); pieces != nil {
		err := ctx.DoIMAP(func(c *imapclient.Client) error {
			var err error
			part, err = joinedHTML(c, mboxName, uid, pieces)
			return err
		})
		if err != nil {
			return err
		}
	}

	asText := ctx.QueryParam("text") == "1" && strings.EqualFold(mimeType, "text/html")
	if asText {
		if part, err = htmlAsText(part); err != nil {
			return err
		}
	}

	view, err := p.viewMessagePart(ctx, msg, partPath, part)
	if err != nil {
		view = View{}
		if err != ErrViewUnsupported {
			ctx.Logger().Printf("message viewer: %v", err)
		}
	}

	flags := make(map[imap.Flag]bool)
	for _, f := range permanentFlags {
		if f == imap.FlagWildcard {
			continue
		}
		flags[f] = msg.HasFlag(f)
	}

	trust := p.newDeliveryTrust(ctx, settings, ctx.Login.Username())
	// The alias shows only where To and Cc do not already name it.
	deliveredTo := trust.alias(msg)
	if addressed(deliveredTo, msg.Envelope.To, msg.Envelope.Cc) {
		deliveredTo = ""
	}
	ibase := assembleIMAPBase(ctx, alborz.NewBaseRenderData(ctx), mboxName, sb, railView)
	ibase.SidebarAccounts = p.sidebarAccounts(ctx)
	ibase.BaseRenderData.WithTitle(msg.Envelope.Subject).WithItem()
	mbox := ibase.Mailbox

	contextURL := *ctx.Request().URL
	data := &MessageRenderData{
		ContextPending:     deferContext,
		ContextURL:         template.URL(contextURL.RequestURI()),
		IMAPBaseRenderData: *ibase,
		Message:            msg,
		Part:               msg.PartByPath(partPath),
		AsText:             asText,
		View:               view.Body,
		MailboxPage:        int(*mbox.NumMessages-msg.SeqNum) / messagesPerPage,
		Flags:              flags,
		NewerURL:           neighbourURL(ctx, mbox.Name(), place.newer),
		OlderURL:           neighbourURL(ctx, mbox.Name(), place.older),
		Position:           place.position,
		Total:              place.total,
		Query:              query,
		Signature:          signature,
		AuthResults:        authResults,
		Warnings:           warnings,
		Mark:               mark,
		InReplyTo:          inReplyTo,
		Answers:            answers,
		ThreadSupported:    threadAlgorithm != "",
		Crumb:              mailboxCrumb(sb.mailboxes, mboxName, ctx.Login.Username()),
		PreferHTML:         ctx.Reading().PreferHTML,
		Unsubscribe:        unsubscribeHref(settings, trust, msg),
		DeliveredTo:        deliveredTo,
		ForwardedBy:        ForwardedBy(msg.rootHeader, trusted, msg.ListID),

		HasRemoteResources:     view.Remote,
		RemoteResourcesAllowed: ctx.QueryParam("allow-remote-resources") == "1",
		Filters: slices.ContainsFunc(ctx.Logins(), func(s *alborz.Login) bool {
			return ctx.Server.SieveEnabled(s.Domain())
		}),
	}
	data.Invitation = messageInvitation(ctx, msg, mboxName, uid)
	for _, add := range ctx.Server.OnMessage {
		if err := add(ctx, data); err != nil {
			return err
		}
	}
	return ctx.Render(http.StatusOK, "message.html", data)
}

// UnsubscribeExternal reports whether the unsubscribe link leaves alborz.
func (d *MessageRenderData) UnsubscribeExternal() bool {
	return d.Unsubscribe != "" && !strings.HasPrefix(d.Unsubscribe, "/")
}

func unsubscribeHref(settings *Settings, trust deliveryTrust, msg *IMAPMessage) string {
	href := msg.ListUnsubscribe
	if href == "" || !strings.HasPrefix(href, "/compose?") {
		return href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	q := u.Query()
	// The request leaves from the account the list wrote to, not the browser's active one.
	q.Set("account", trust.owner(msg))
	if from := writeAs(settings, trust, msg, msg.Envelope.To, msg.Envelope.Cc); from != "" {
		q.Set("from", from)
	}
	u.RawQuery = alborz.Query(q)
	return u.String()
}

// An unparsable invitation is not a page failure: the part is still an attachment.
func messageInvitation(ctx *alborz.Context, msg *IMAPMessage, mboxName string, uid imap.UID) *Invitation {
	inv, _, err := invitationAt(ctx, msg, mboxName, uid)
	if err != nil {
		ctx.Logger().Printf("failed to read the calendar part: %v", err)
		return nil
	}
	return inv
}

// PartAt reads one part of a message as the bytes it was sent as, with its media type.
func PartAt(ctx *alborz.Context, mboxName string, uid imap.UID, part string) ([]byte, string, error) {
	partPath, err := parsePartPath(part)
	if err != nil {
		return nil, "", echo.NewHTTPError(http.StatusBadRequest, err)
	}
	var raw []byte
	var mediaType string
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		_, entity, err := getMessagePart(c, mboxName, uid, partPath)
		if err != nil {
			return err
		}
		mediaType, _, _ = entity.Header.ContentType()
		raw, err = io.ReadAll(entity.Body)
		return err
	})
	return raw, mediaType, err
}

// InvitationAt reads the scheduling part of one message, and the bytes it was written as.
func InvitationAt(ctx *alborz.Context, mboxName string, uid imap.UID) (*Invitation, []byte, error) {
	var msg *IMAPMessage
	if err := ctx.DoIMAP(func(c *imapclient.Client) error {
		var err error
		msg, _, err = getMessagePart(c, mboxName, uid, nil)
		return err
	}); err != nil {
		return nil, nil, err
	}
	return invitationAt(ctx, msg, mboxName, uid)
}

func invitationAt(ctx *alborz.Context, msg *IMAPMessage, mboxName string, uid imap.UID) (*Invitation, []byte, error) {
	part := invitationPart(msg)
	if part == nil {
		return nil, nil, nil
	}
	var raw []byte
	err := ctx.DoIMAP(func(c *imapclient.Client) error {
		_, entity, err := getMessagePart(c, mboxName, uid, part.Path)
		if err != nil {
			return err
		}
		raw, err = io.ReadAll(entity.Body)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return readInvitation(raw, part.PathString(), ctx.Login.Username()), raw, nil
}

// The URI is re-read from the message, so a page cannot make us post anywhere (RFC 8058).
func handleUnsubscribe(ctx *alborz.Context) error {
	mboxName, uid, err := messageRef(ctx)
	if err != nil {
		return err
	}
	var msg *IMAPMessage
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		var err error
		msg, _, err = getMessagePart(c, mboxName, uid, nil)
		return err
	})
	if err != nil {
		return err
	}
	back := ctx.NextOr(ctx.AccountPath(fmt.Sprintf("/message/%v/%v",
		url.PathEscape(mboxName), uid)))
	if msg.OneClick == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "this message offers no one-click unsubscribe")
	}

	body := strings.NewReader("List-Unsubscribe=One-Click")
	req, err := http.NewRequestWithContext(ctx.Request().Context(),
		http.MethodPost, msg.OneClick, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := ctx.Server.Outgoing.Remote(unsubscribeTimeout).Do(req)
	if err != nil {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.T("notice.unsubfailed")})
		return ctx.Redirect(http.StatusFound, back)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		ctx.Logger().Printf("unsubscribe %s answered %s", msg.OneClick, resp.Status)
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeFailed, Text: ctx.T("notice.unsubfailed")})
		return ctx.Redirect(http.StatusFound, back)
	}
	ctx.PutNotice(ctx.T("notice.unsubscribed"))
	return ctx.Redirect(http.StatusFound, back)
}

const unsubscribeTimeout = 15 * time.Second

func sentFolder(mailboxes []MailboxInfo) string {
	for i := range mailboxes {
		if mailboxes[i].role() == "sent" {
			return mailboxes[i].Name()
		}
	}
	return ""
}
