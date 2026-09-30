package mail

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
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
	"git.mehdix.org/alborz/web"
	"github.com/alborzmail/go-itip/imip"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"
	"jaytaylor.com/html2text"
)

type MessageRenderData struct {
	IMAPBaseRenderData
	Shown
	Part *IMAPPartNode
	// AsText is the HTML part read as words, offered only without a plain part.
	AsText         bool
	View           interface{}
	MailboxPage    int
	Flags          map[imap.Flag]bool
	ContextPending bool
	ContextURL     template.URL

	// AuthResults is what the receiving server said about the sender's domain.
	AuthResults *AuthResults
	// Warnings are the indicators that earned a colour, and Mark the colour.
	Warnings []Indicator
	Mark     Grade

	// Unsubscribe is where the unsubscribe control sends the reader.
	Unsubscribe string
	// List are the list's links as the page gives them.
	List listLinks

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

	// Neighbours in the list the message was opened from.
	Neighbours web.Neighbours
	Query      string

	// HasRemoteResources says the shown part asks for resources elsewhere, loaded only when RemoteResourcesAllowed.
	HasRemoteResources     bool
	RemoteResourcesAllowed bool

	// Filters offers the rule shortcuts, where an account has a filter server.
	Filters bool
}

// Shown is a message on its page as the other features see it: they read Message and fill what they
// offer beside it (web.Extend).
type Shown struct {
	Message *IMAPMessage
	// Filled by calendars and contacts, nil where they have nothing: the scheduling request this
	// message carries, the share it announces, where an invitation or an attached file can be filed,
	// and the stored copy a withdrawal can drop.
	Invitation      any
	ShareOffer      any
	InviteCalendars any
	ImportCalendars any
	ImportBooks     any
	InviteFiled     string
	// Contact is what "Add to contacts" offers; nil where no address book takes a card.
	Contact *ContactOffer
}

// ContactOffer is the message's addresses and the address books one can be added to.
type ContactOffer struct {
	Addresses []*mail.Address
	Books     any
}

// Enough for mail with a few files, while mail with fifty keeps the body in view.
const attachmentsShown = 5

// AttachmentsOpen reports whether the card starts open.
func (d *MessageRenderData) AttachmentsOpen() bool {
	return len(d.Message.Attachments()) <= attachmentsShown
}

// With plain the bytes are shown rather than saved: the source view.
func handleDownloadMessage(ctx *web.Context, plain bool) error {
	mboxName, uid, err := MessageRef(ctx)
	if err != nil {
		return err
	}
	return streamed(ctx, streamMessage(ctx.Request().Context(), ctx.Login, mboxName, uid, func(env *imap.Envelope, lit imap.LiteralReader) error {
		ctx.Response().Header().Set("Content-Length", fmt.Sprint(lit.Size()))
		if plain {
			// No charset can be right for a whole message whose parts each name their own.
			return ctx.Stream(http.StatusOK, "text/plain; charset=utf-8", lit)
		}
		ctx.Response().Header().Set("Content-Disposition", downloadName(env.Subject, fmt.Sprint(uid), ".eml"))
		return ctx.Stream(http.StatusOK, "message/rfc822", lit)
	}))
}

// streamMessage hands write the whole message as the server sends it, on a connection of its own.
func streamMessage(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID, write func(*imap.Envelope, imap.LiteralReader) error) error {
	return login.DoIMAPTransfer(ctx, func(c *imapclient.Client) error {
		env, err := fetchEnvelope(c, mboxName, uid)
		if err != nil {
			return err
		}
		whole := &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{{Peek: true}}}
		return streamSection(c, uid, whole, func(lit imap.LiteralReader) error {
			return write(env, lit)
		})
	})
}

// streamed aborts a response already under way, since it can no longer turn
// into an error page; ended cleanly, a short file would look complete.
func streamed(ctx *web.Context, err error) error {
	if err != nil && ctx.Response().Committed {
		ctx.Logger().Printf("%s: %v", ctx.Request().URL.Path, err)
		panic(http.ErrAbortHandler)
	}
	return err
}

func fetchEnvelope(c *imapclient.Client, mboxName string, uid imap.UID) (*imap.Envelope, error) {
	if err := ensureMailboxSelected(c, mboxName); err != nil {
		return nil, err
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, Envelope: true}).Collect()
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, alborz.NotFound("notfound.message", fmt.Sprint(uid))
	}
	return msgs[0].Envelope, nil
}

func handleDownloadAttachments(ctx *web.Context) error {
	mboxName, uid, err := MessageRef(ctx)
	if err != nil {
		return err
	}
	return streamed(ctx, zipAttachments(ctx.Request().Context(), ctx.Login, mboxName, uid, func(env *imap.Envelope) io.Writer {
		ctx.Response().Header().Set("Content-Disposition", downloadName(env.Subject, fmt.Sprint(uid), ".zip"))
		ctx.Response().Header().Set("Content-Type", "application/zip")
		ctx.Response().WriteHeader(http.StatusOK)
		return ctx.Response()
	}))
}

// zipAttachments writes a message's attachments as a zip archive into what open gives once the message reads.
func zipAttachments(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID, open func(*imap.Envelope) io.Writer) error {
	return streamMessage(ctx, login, mboxName, uid, func(env *imap.Envelope, lit imap.LiteralReader) error {
		entity, err := message.Read(lit)
		if err != nil && !message.IsUnknownCharset(err) {
			return err
		}
		archive := zip.NewWriter(open(env))
		if err := writeAttachments(archive, entity, map[string]int{}, ""); err != nil {
			return err
		}
		return archive.Close()
	})
}

func singlePart(c *imapclient.Client, mboxName string, uid imap.UID, partPath []int) (*imap.BodyStructureSinglePart, error) {
	if err := ensureMailboxSelected(c, mboxName); err != nil {
		return nil, err
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		UID: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}).Collect()
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, alborz.NotFound("notfound.message", fmt.Sprint(uid))
	}
	var part *imap.BodyStructureSinglePart
	msgs[0].BodyStructure.Walk(func(p []int, bs imap.BodyStructure) bool {
		if single, ok := bs.(*imap.BodyStructureSinglePart); ok && slices.Equal(p, partPath) {
			part = single
		}
		return part == nil
	})
	if part == nil {
		return nil, alborz.NotFound("notfound.part", fmt.Sprint(uid))
	}
	return part, nil
}

// handleDownloadPart streams one part, decoded, as it comes from the server.
func handleDownloadPart(ctx *web.Context, mboxName string, uid imap.UID, partPath []int) error {
	return streamed(ctx, streamPart(ctx.Request().Context(), ctx.Login, mboxName, uid, partPath, func(part *imap.BodyStructureSinglePart, size int64, body io.Reader) error {
		mimeType := strings.ToLower(part.MediaType())
		contentType := mimeType
		if charset := part.Params["charset"]; charset != "" && part.Type == "text" {
			contentType = mime.FormatMediaType(mimeType, map[string]string{"charset": charset})
		}
		var disposition string
		if d := part.Disposition(); d != nil {
			disposition = d.Value
		}
		// Never serve text/html and the like inline.
		if mimeType != "text/plain" || strings.EqualFold(disposition, "attachment") {
			ctx.Response().Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
				"filename": zipEntryName(newIMAPPartNode(nil, partPath, part).Filename, mimeType, map[string]int{}),
			}))
		}
		if size >= 0 {
			ctx.Response().Header().Set("Content-Length", fmt.Sprint(size))
		}
		return ctx.Stream(http.StatusOK, contentType, body)
	}))
}

// streamPart hands write one part, decoded, and its length where the server knows it, else -1.
func streamPart(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID, partPath []int, write func(part *imap.BodyStructureSinglePart, size int64, body io.Reader) error) error {
	return login.DoIMAPTransfer(ctx, func(c *imapclient.Client) error {
		part, err := singlePart(c, mboxName, uid, partPath)
		if err != nil {
			return err
		}
		// BINARY (RFC 3516) has the server decode the part, and so know its length.
		options := &imap.FetchOptions{UID: true}
		binary := c.Caps().Has(imap.CapBinary)
		if binary {
			options.BinarySection = []*imap.FetchItemBinarySection{{Part: partPath}}
		} else {
			options.BodySection = []*imap.FetchItemBodySection{{Part: partPath}}
		}
		return streamSection(c, uid, options, func(lit imap.LiteralReader) error {
			if binary {
				return write(part, lit.Size(), lit)
			}
			return write(part, -1, transferDecoded(part.Encoding, lit))
		})
	})
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
				&imap.FetchItemBodySection{Peek: true, Part: p.Path, Partial: &imap.SectionPartial{Size: partReadMax}})
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
		piece, err := readPart(h, bodyBuf, p.Cut())
		if err != nil {
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
		// Only the header is read for the name; what it took is put back in front.
		var head bytes.Buffer
		inner, err := textproto.ReadHeader(bufio.NewReader(io.TeeReader(e.Body, &head)))
		if err == nil {
			h := mail.Header{Header: message.Header{Header: inner}}
			subject, _ := h.Subject()
			filename = attachmentName(subject)
		}
		e.Body = io.MultiReader(&head, e.Body)
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

// handleRaw serves a part, or the whole message without one, as its bytes.
func handleRaw(ctx *web.Context) error {
	if ctx.QueryParam("part") == "" {
		return handleDownloadMessage(ctx, ctx.QueryParam("plain") == "1")
	}
	mboxName, uid, err := MessageRef(ctx)
	if err != nil {
		return err
	}
	partPath, err := ParsePartPath(ctx.QueryParam("part"))
	if err != nil {
		return web.NewError(http.StatusBadRequest, err)
	}
	return handleDownloadPart(ctx, mboxName, uid, partPath)
}

func (p *plugin) handleGetPart(ctx *web.Context) error {
	if ctx.PartialFor("message-navigation") {
		return p.handleMessageNavigation(ctx)
	}
	mboxName, uid, err := MessageRef(ctx)
	if err != nil {
		return err
	}

	partPath, err := ParsePartPath(ctx.QueryParam("part"))
	if err != nil {
		return web.NewError(http.StatusBadRequest, err)
	}

	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	messagesPerPage := web.PerPage(ctx)

	query := ctx.QueryParam("query")
	railView, err := readView(ctx.QueryParams())
	if err != nil {
		return err
	}

	// A list across accounts or folders is read back through its own pages, not this folder's order.
	merged := mergedOrigin(ctx)
	shown, err := p.read(ctx.Request().Context(), messageRead{
		login: ctx.Login, logins: ctx.Logins(), settings: settings,
		mailbox: mboxName, uid: uid, part: partPath, named: ctx.QueryParam("part") != "",
		list:    folderList{query, railView, ctx.QueryParam("sort"), ctx.QueryParam("dir")},
		perPage: messagesPerPage, preferHTML: ctx.Reading().PreferHTML, partial: ctx.Partial(), merged: merged != nil,
		moneyWords: strings.Split(ctx.T("indicator.moneywords"), ","),
	})
	if err != nil {
		return err
	}
	msg, part, partPath, sb, place := shown.msg, shown.part, shown.partPath, shown.sb, shown.place
	permanentFlags, authResults, trusted := shown.permanentFlags, shown.authResults, shown.trusted
	inReplyTo, answers, threadAlgorithm, deferContext := shown.inReplyTo, shown.answers, shown.threadAlgorithm, shown.deferContext
	warnings, mark := shown.warnings, shown.mark

	// The bare envelope has no viewer.
	if len(partPath) == 0 {
		preferred := msg.PreferredPart(ctx.Reading().PreferHTML)
		if preferred != nil && len(preferred.Path) > 0 {
			q := ctx.Request().URL.Query()
			q.Set("part", preferred.PathString())
			return ctx.Redirect(http.StatusFound, ctx.Path("mail.message", msg.Mailbox, msg.UID)+"?"+web.Query(q))
		}
	}

	if len(partPath) > 0 && msg.PartByPath(partPath) == nil {
		return web.NewError(http.StatusNotFound,
			fmt.Sprintf("message %v has no part %v", uid, ctx.QueryParam("part")))
	}

	mimeType, _, err := part.Header.ContentType()
	if err != nil {
		return fmt.Errorf("failed to parse part Content-Type: %v", err)
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

	view, err := viewMessagePart(ctx, msg, partPath, part)
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

	trust := p.newDeliveryTrust(ctx.Login, settings)
	// The alias shows only where To and Cc do not already name it.
	deliveredTo := trust.alias(msg)
	if addressed(deliveredTo, msg.Envelope.To, msg.Envelope.Cc) {
		deliveredTo = ""
	}
	ibase := assembleIMAPBase(ctx, web.NewBaseRenderData(ctx), mboxName, sb, railView)
	ibase.SidebarAccounts = p.sidebarAccounts(ctx)
	ibase.BaseRenderData.WithTitle(msg.Envelope.Subject).WithItem()
	mbox := ibase.Mailbox

	nav := neighbours(ctx, mbox.Name(), place)
	if merged != nil && !deferContext {
		if nav, err = p.mergedNeighbours(ctx, settings, merged, mboxName, uid); err != nil {
			return err
		}
	}
	contextURL := *ctx.Request().URL
	data := &MessageRenderData{
		ContextPending:     deferContext,
		ContextURL:         template.URL(contextURL.RequestURI()),
		IMAPBaseRenderData: *ibase,
		Shown:              Shown{Message: msg},
		Part:               msg.PartByPath(partPath),
		AsText:             asText,
		View:               view.Body,
		MailboxPage:        int(*mbox.NumMessages-msg.SeqNum) / messagesPerPage,
		Flags:              flags,
		Neighbours:         nav,
		Query:              query,
		AuthResults:        authResults,
		Warnings:           warnings,
		Mark:               mark,
		InReplyTo:          inReplyTo,
		Answers:            answers,
		ThreadSupported:    threadAlgorithm != "",
		Crumb:              mailboxCrumb(sb.mailboxes, mboxName, ctx.Login.Username(), folderPath(ctx)),
		PreferHTML:         ctx.Reading().PreferHTML,
		Unsubscribe:        unsubscribeHref(ctx, settings, trust, msg),
		List: listLinks{
			Help: listHref(ctx, msg.ListHelp), Subscribe: listHref(ctx, msg.ListSubscribe), Owner: listHref(ctx, msg.ListOwner),
		},
		DeliveredTo: deliveredTo,
		ForwardedBy: ForwardedBy(msg.rootHeader, trusted, msg.ListID),

		HasRemoteResources:     view.Remote,
		RemoteResourcesAllowed: ctx.QueryParam("allow-remote-resources") == "1",
		Filters: slices.ContainsFunc(ctx.Logins(), func(s *alborz.Login) bool {
			return ctx.Server.SieveEnabled(s.Domain())
		}),
	}
	if err := web.Extended(ctx, &data.Shown); err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "mail/message", data)
}

// UnsubscribeExternal reports whether the unsubscribe link leaves alborz.
func (d *MessageRenderData) UnsubscribeExternal() bool {
	return d.Unsubscribe != "" && Mailto(d.Message.ListUnsubscribe) == nil
}

func unsubscribeHref(ctx *web.Context, settings *Settings, trust deliveryTrust, msg *IMAPMessage) string {
	if q := unsubscribeMail(settings, trust, msg); q != nil {
		return ctx.Path("mail.compose") + "?" + web.Query(q)
	}
	return msg.ListUnsubscribe
}

// unsubscribeMail is what compose takes to leave a list by mail; nil where the list takes none.
func unsubscribeMail(settings *Settings, trust deliveryTrust, msg *IMAPMessage) url.Values {
	q := Mailto(msg.ListUnsubscribe)
	if q == nil {
		return nil
	}
	// The request leaves from the account the list wrote to, not the browser's active one.
	q.Set("account", trust.owner(msg))
	if from := writeAs(settings, trust, msg, msg.Envelope.To, msg.Envelope.Cc); from != "" {
		q.Set("from", from)
	}
	return q
}

// listLinks are where a list's links take the reader.
type listLinks struct{ Help, Subscribe, Owner string }

// A mailto opens this account's compose page, not whatever mail app the machine has.
func listHref(ctx *web.Context, uri string) string {
	if q := Mailto(uri); q != nil {
		return ctx.Path("mail.compose") + "?" + web.Query(q)
	}
	return uri
}

// PartAt reads one part of a message as the bytes it was sent as, with its media type.
func PartAt(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID, partPath []int) ([]byte, string, error) {
	var raw []byte
	var mediaType string
	err := login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
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

// CalendarPartAt reads the scheduling message of one mail, and the part it is.
func CalendarPartAt(ctx context.Context, login *alborz.Login, mboxName string, uid imap.UID) (*ical.Calendar, string, error) {
	var msg *IMAPMessage
	if err := login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}
		msgs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
			UID: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		}).Collect()
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return alborz.NotFound("notfound.message", fmt.Sprint(uid))
		}
		msg = &IMAPMessage{FetchMessageBuffer: msgs[0], Mailbox: mboxName}
		return nil
	}); err != nil {
		return nil, "", err
	}
	return CalendarPart(ctx, login, msg, mboxName, uid)
}

// CalendarPart reads the scheduling message of a fetched mail; nil for none.
func CalendarPart(ctx context.Context, login *alborz.Login, msg *IMAPMessage, mboxName string, uid imap.UID) (*ical.Calendar, string, error) {
	part := invitationPart(msg)
	if part == nil || part.Cut() {
		return nil, "", nil
	}
	var cal *ical.Calendar
	err := login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}
		msgs, err := c.Fetch(imap.UIDSetNum(uid), partFetchOptions(part.Path, true, true)).Collect()
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return alborz.NotFound("notfound.message", fmt.Sprint(uid))
		}
		_, entity, err := messagePart(msgs[0], mboxName, uid, part.Path, true)
		if err != nil {
			return err
		}
		cals, err := imip.Read(entity)
		if len(cals) > 0 {
			cal = cals[0]
		}
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return cal, part.PathString(), nil
}

// The URI is re-read from the message, so a page cannot make us post anywhere (RFC 8058).
func (p *plugin) handleUnsubscribe(ctx *web.Context) error {
	mboxName, uid, err := MessageRef(ctx)
	if err != nil {
		return err
	}
	var oneClick string
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		var err error
		oneClick, err = oneClickURI(c, mboxName, uid)
		return err
	})
	if err != nil {
		return err
	}
	back := ctx.NextOr(ctx.URL("mail.message", mboxName, uid))
	if oneClick == "" {
		return web.NewError(http.StatusBadRequest, "this message offers no one-click unsubscribe")
	}
	if err := p.unsubscribe(ctx.Request().Context(), oneClick); err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.T("notice.unsubfailed")})
		return ctx.Redirect(http.StatusFound, back)
	}
	ctx.PutNotice(ctx.T("notice.unsubscribed"))
	return ctx.Redirect(http.StatusFound, back)
}

// oneClickURI is where a message's list takes a one-click unsubscribe (RFC 8058); empty where it offers none.
func oneClickURI(c *imapclient.Client, mboxName string, uid imap.UID) (string, error) {
	if err := ensureMailboxSelected(c, mboxName); err != nil {
		return "", err
	}
	header := &imap.FetchItemBodySection{Peek: true, Specifier: imap.PartSpecifierHeader,
		HeaderFields: []string{"List-Unsubscribe", "List-Unsubscribe-Post"}}
	msgs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{header},
	}).Collect()
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return "", alborz.NotFound("notfound.message", fmt.Sprint(uid))
	}
	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(msgs[0].FindBodySection(header))))
	if err != nil {
		return "", err
	}
	msg := &IMAPMessage{Mailbox: mboxName}
	msg.setListHeaders(h)
	return msg.OneClick, nil
}

// unsubscribe posts the one-click unsubscribe to the list's address.
func (p *plugin) unsubscribe(ctx context.Context, uri string) error {
	body := strings.NewReader("List-Unsubscribe=One-Click")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uri, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.server.Outgoing.Remote(unsubscribeTimeout).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		p.server.Logger().Printf("unsubscribe %s answered %s", uri, resp.Status)
		return fmt.Errorf("unsubscribe %s answered %s", uri, resp.Status)
	}
	return nil
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

// messageRead asks for one message as its page shows it.
type messageRead struct {
	login *alborz.Login
	// logins are the accounts signed in: each one's senders tell an imitation.
	logins   []*alborz.Login
	settings *Settings
	mailbox  string
	uid      imap.UID
	// part is the part asked for, nil for the one the rows open; named says the address named one.
	part  []int
	named bool
	list  folderList
	// perPage is the list's page size; partial says the page is swapped in place, so its context can follow.
	perPage    int
	preferHTML bool
	partial    bool
	// merged says the list the message was opened from spans accounts or folders, and is read back elsewhere.
	merged     bool
	moneyWords []string
}

// messageShown is a message read for its page, from what is held where it can be.
type messageShown struct {
	msg             *IMAPMessage
	part            *message.Entity
	partPath        []int
	sb              sidebar
	permanentFlags  []imap.Flag
	authResults     *AuthResults
	trusted         string
	inReplyTo       *ThreadNeighbour
	answers         []ThreadNeighbour
	threadAlgorithm imap.ThreadAlgorithm
	place           listPlace
	// deferContext says the place and the thread are left for the page to ask for.
	deferContext bool
	warnings     []Indicator
	mark         Grade
}

func (p *plugin) read(ctx context.Context, r messageRead) (*messageShown, error) {
	// Read before the message's IMAP work: it may sample the inbox, and the login lock is not reentrant.
	trusted := p.TrustedAuthServ(r.login, r.settings)
	partPath := r.part
	cached, place, placed := p.cachedPlace(r.login.Username(), r.mailbox, r.uid, r.list, r.perPage)
	// A list across accounts or folders is read back through its own pages, not this folder's order.
	if r.merged {
		place, placed = listPlace{}, true
	}
	// A link naming no part opens the part the rows would link to; the cached row knows it without a fetch.
	if !r.named && cached != nil {
		if row := cached.row(r.uid); row != nil {
			if preferred := row.PreferredPart(r.preferHTML); preferred != nil && len(preferred.Path) > 0 {
				partPath = preferred.Path
			}
		}
	}
	held := p.bodies.current(r.login.Username(), r.mailbox, r.uid, partPath)
	if held != nil && len(partPath) == 0 {
		partPath = held.part
	}
	alborz.CacheTiming(ctx, "body", held != nil)
	var (
		sb              sidebar
		msg             *IMAPMessage
		part            *message.Entity
		permanentFlags  []imap.Flag
		authResults     *AuthResults
		inReplyTo       *ThreadNeighbour
		answers         []ThreadNeighbour
		threadAlgorithm imap.ThreadAlgorithm
		err             error
	)
	// A flag change evicts the listing but not the held body, so the sidebar may load below.
	if cached != nil {
		sb = cached.sb
	}
	if held != nil && cached == nil {
		sb, err = p.sidebarFor(r.login)
		if err != nil {
			return nil, err
		}
		sb = sb.clone()
		sb.active = sb.statuses[r.mailbox]
	}
	if held != nil {
		if msg, part, err = messagePart(held.buf, r.mailbox, r.uid, partPath, true); err != nil {
			return nil, err
		}
		permanentFlags = held.permanent
		authResults = readAuthResults(messageRootHeader(msg), trusted)
		// The peek left it unread on the server; the page is what reads it.
		if !msg.HasFlag(imap.FlagSeen) {
			p.markHeldSeen(r.login, r.mailbox, r.uid, held.validity)
		}
	}
	sent := sentFolder(sb.mailboxes)
	needsThread := msg != nil && (len(msg.Envelope.InReplyTo) > 0 || msg.References != "" || (sent != "" && r.mailbox == sent))
	deferContext := held != nil && sb.active != nil && r.partial && (!placed || needsThread || r.merged)
	if cached != nil {
		threadAlgorithm = cached.threadAlgorithm
	}
	if held == nil || sb.active == nil || (!deferContext && (!placed || needsThread)) {
		err = r.login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
			var load *sidebarLoad
			var err error
			if sb.active == nil {
				if load, err = startSidebar(c, r.mailbox, r.mailbox, r.settings.Subscriptions); err != nil {
					return err
				}
			}
			if held == nil {
				if msg, part, err = getShownPart(c, r.mailbox, r.uid, partPath); err != nil {
					return err
				}
				// The cached listings are marked read rather than fetched again for one flag.
				p.messageRead(r.login.Username(), r.mailbox, r.uid)
				permanentFlags = c.Mailbox().PermanentFlags
			}
			if !placed {
				if place, err = p.placeInList(c, r.login.Username(), r.settings, r.mailbox, r.uid, r.list); err != nil {
					return err
				}
			}
			threadAlgorithm = ThreadAlgorithm(c)
			if load != nil {
				sent = sentFolder(load.sb.mailboxes)
			}
			inReplyTo, answers = readThread(p.server.Logger(), c, r.mailbox, sent, msg)
			if held == nil {
				authResults = readAuthResults(messageRootHeader(msg), trusted)
			}
			if load != nil {
				sb, err = load.finish()
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	// Not in Junk: a message filed as junk needs no telling.
	var indicators []Indicator
	if folderRole(sb.mailboxes, r.mailbox) != "junk" {
		evidence := &Evidence{
			Header:     messageRootHeader(msg),
			Trusted:    trusted,
			Auth:       authResults,
			Subject:    msg.Envelope.Subject,
			From:       envelopeSender(msg.Envelope),
			MoneyWords: r.moneyWords,
		}
		if evidence.From != "" {
			// Every signed-in account's inbox is asked: an imitation may reach a different address.
			for _, s := range r.logins {
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
	return &messageShown{msg: msg, part: part, partPath: partPath, sb: sb, permanentFlags: permanentFlags,
		authResults: authResults, trusted: trusted, inReplyTo: inReplyTo, answers: answers,
		threadAlgorithm: threadAlgorithm, place: place, deferContext: deferContext, warnings: warnings, mark: mark}, nil
}
