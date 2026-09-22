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
)

type MessageRenderData struct {
	IMAPBaseRenderData
	Message        *IMAPMessage
	Part           *IMAPPartNode
	View           interface{}
	MailboxPage    int
	Flags          map[imap.Flag]bool
	ContextPending bool
	ContextURL     template.URL

	// Invitation is the scheduling request this message carries, nil when
	// it carries none.
	Invitation *Invitation

	// AuthResults is what the receiving server said about the sender's
	// domain, nil when no trusted server reported or none is named.
	AuthResults *AuthResults
	// Relation is what the reader's folders say about the sender, for
	// the line under the address: the list's tag leads to it, so it is
	// on the page whether or not the message earned a card.
	Relation *Relation
	// Warnings are the indicators that earned a colour, and Mark the
	// colour: none, caution or alarm. Nothing is said about a message
	// with nothing against it.
	Warnings []Indicator
	Mark     Grade

	// Unsubscribe is where the unsubscribe control sends the reader. It
	// is the list's own page when the list gave one, and otherwise our
	// compose form with the identity the message was delivered to
	// already chosen: a list keyed on an alias does not recognise a
	// request from the account behind it.
	Unsubscribe string

	// InReplyTo is the message this one answers, when it is in the same
	// folder. It is nil when the server would not search headers.
	InReplyTo *ThreadNeighbour

	// Answers are the messages answering this one, found only for mail
	// being read in Sent: elsewhere the answer is already in the folder.
	Answers []ThreadNeighbour

	// ThreadSupported says the server can group the folder into
	// conversations, which is the only thing that makes the link to
	// them worth offering.
	ThreadSupported bool

	// Crumb is the path to the folder this message is in, account first.
	Crumb []CrumbLink
	// PreferHTML is the account's choice of which part the tabs open.
	PreferHTML bool

	// DeliveredTo names the address the message was delivered to when
	// that is not the account it landed in - the alias, in other words.
	DeliveredTo string

	// ForwardedBy names the mailbox that passed the message on, proved
	// by the SPF check our own server made. Empty unless it is certain.
	ForwardedBy string

	// Neighbors in the view the message was opened from; nil when absent.
	NewerURL *url.URL
	OlderURL *url.URL
	Position int
	Total    int
	Query    string

	// Signature is what the page says about the message's authenticity,
	// as chrome outside the body frame. Its zero value says nothing,
	// which is what an unsigned message deserves.
	Signature Verification
}

// attachmentsShown is how many files the card shows without being
// asked: enough for the mail that carries a few, while the mail that
// carries fifty keeps the body in view.
const attachmentsShown = 5

// AttachmentsOpen reports whether the card starts open.
func (d *MessageRenderData) AttachmentsOpen() bool {
	return len(d.Message.Attachments()) <= attachmentsShown
}

// handleInvitationReply answers a meeting request by mail, which is
// what the organizer's client is waiting for (RFC 6047). The invitation
// is re-read from the message rather than taken from the form: what is
// answered has to be what was sent.
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

// handleDownloadMessage hands over the bytes the server holds. It is
// what makes a message filable, forwardable to somebody else's tooling,
// and checkable against what was actually stored, none of which a
// rendered page can do.
// handleDownloadMessage sends the message as the server holds it, byte
// for byte. With plain it is shown rather than saved: the source view.
// Parsing the message and writing it out again is not the message, and
// go-message will not write text in a charset other than UTF-8, so a
// source view built that way answered 500 to every ISO-8859-1 mail.
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
		// No charset can be right for a whole message, whose parts each
		// name their own; UTF-8 is what 8-bit mail is written in, and a
		// header is ASCII whatever is said here.
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

// handleDownloadAttachments sends every attachment of one message as a
// zip, so a message holding fifty files is one click rather than fifty.
// It writes as it walks: the parts are already in memory, the archive
// need not be.
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
		// The reader has bytes by now, so the file ends short rather
		// than turning into an error page.
		ctx.Logger().Printf("attachments %q uid %v: %v", mboxName, uid, err)
	}
	return archive.Close()
}

// joinedHTML is one document of the pieces HTMLPieces names, fetched in
// one exchange. An image is referred to by its path, which the viewer
// resolves to the part as it does a Content-ID.
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

// writeAttachments puts every attached part into the archive, naming an
// unnamed one after the extension its type gives. What is attached is
// what the message's card counts (Attachments): a part that attached
// calls a file, at any depth, and the message itself when it is
// nothing else. parent is the subtype of the multipart e sits in.
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

// zipEntryName keeps the file's own name where it has one, and keeps
// names apart: a message may attach the same name twice.
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

func handleGetPart(ctx *alborz.Context, raw bool) error {
	if !raw && ctx.PartialFor("message-navigation") {
		return handleMessageNavigation(ctx)
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

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	// Read before the message's own IMAP work: the observed id may cost
	// a sample of the inbox, and the session lock is not reentrant.
	trusted := TrustedAuthServ(ctx, settings)
	messagesPerPage := PerPage(ctx)

	query := ctx.QueryParam("query")
	railView, err := readView(ctx)
	if err != nil {
		return err
	}

	// The rendered view needs the sidebar, and takes it from the cached
	// listing the reader came from, with the message's place in the
	// page. Without one its mailbox LIST is issued with the message's
	// SELECT pipelined behind it, and its STATUS responses ride along
	// with the message fetch. A raw download only selects to fetch.
	var (
		cached *listingEntry
		place  listPlace
		placed bool
	)
	if !raw {
		cached, place, placed = cachedPlace(ctx, mboxName, uid, query, railView)
	}
	// A link naming no part, like Newer and Older, opens the part the
	// mailbox rows would link to. The cached row knows the structure,
	// so the answer costs no fetch.
	if !raw && ctx.QueryParam("part") == "" && cached != nil {
		if row := cached.row(uid); row != nil {
			if preferred := row.PreferredPart(ctx.Reading().PreferHTML); preferred != nil && len(preferred.Path) > 0 {
				partPath = preferred.Path
			}
		}
	}
	// A body fetched ahead of the click serves the page without the
	// server, unless the message is signed: the check reads the signed
	// parts on the connection that holds the folder.
	var held *cachedBody
	if !raw {
		held = bodies.current(ctx.Session.Username(), mboxName, uid, partPath)
		if held != nil && len(partPath) == 0 {
			partPath = held.part
		}
		if held != nil && held.buf.BodyStructure != nil {
			if _, _, signed := signedParts(held.buf.BodyStructure); signed {
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
	// The sidebar comes with the listing when that is cached. A flag
	// change evicts the listing while the body fetched ahead stays
	// held, and the sidebar is then loaded on the connection below.
	if cached != nil {
		sb = cached.sb
	}
	if held != nil && cached == nil {
		sb, err = sidebarFor(ctx.Session)
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
		// What our own server made of SPF, DKIM and DMARC when it took
		// delivery, read from the header the fetch brought.
		authResults = readAuthResults(messageRootHeader(msg), trusted)
		// The peek left the message unread on the server; the page is
		// what reads it, and nothing waits on the server saying so.
		if !msg.HasFlag(imap.FlagSeen) {
			markHeldSeen(ctx.Session, mboxName, uid, held.validity)
		}
	}
	sent := sentFolder(sb.mailboxes)
	// The server is asked for what the cache cannot say: the message
	// itself when it was not fetched ahead, its place at a page's edge,
	// what it answers, and what answered it in Sent.
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
				// The body fetch marked the message read; the cached listings say
				// so too rather than being fetched again for one flag.
				messageRead(ctx.Session.Username(), mboxName, uid)
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
				// Whether the message is from who it says, on the same
				// connection that has the mailbox open. A message nobody
				// signed costs nothing here: signedParts walks a structure
				// already in hand and finds nothing.
				signature = verifySignature(c, mboxName, uid, msg.BodyStructure,
					messageRootHeader(msg), envelopeSender(msg.Envelope))
				// What our own server made of SPF, DKIM and DMARC when
				// it took delivery. Read from the same header set, and
				// only from the instance the trusted server wrote.
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
	// The facts beside the message. The folders are asked about the
	// sender once an hour per address, so the page rarely pays for it.
	// Not in Junk, as the list colours no row there: a message filed as
	// junk needs no telling, and "you junked this sender before" is the
	// folder describing itself.
	var indicators []Indicator
	var relation *Relation
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
			if book := senderBookFor(ctx.Session); book != nil {
				rel := book.relationTo(evidence.From)
				evidence.Relation, relation = &rel, &rel
			}
			// Whom the reader knows does not stop at an account: mail
			// from a host bills one address and its imitation reaches
			// another, so every signed-in account's inbox is asked.
			for _, s := range ctx.Sessions() {
				if book := senderBookFor(s); book != nil && evidence.Named == nil {
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

	// A link naming no part, like Newer and Older, opens the part the
	// mailbox rows would link to; the bare envelope has no viewer.
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

		disp, dispParams, _ := part.Header.ContentDisposition()
		filename := dispParams["filename"]

		// TODO: set Content-Length if possible

		// Be careful not to serve types like text/html as inline
		if !strings.EqualFold(mimeType, "text/plain") || strings.EqualFold(disp, "attachment") {
			dispParams := make(map[string]string)
			if filename != "" {
				dispParams["filename"] = filename
			}
			disp := mime.FormatMediaType("attachment", dispParams)
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

	view, err := viewMessagePart(ctx, msg, partPath, part)
	if err != nil {
		view = nil
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

	trust := newDeliveryTrust(ctx, settings, ctx.Session.Username())
	// The row says where the mail went only where To and Cc do not
	// already: an alias the sender wrote out is on the page once.
	deliveredTo := trust.alias(msg)
	if addressed(deliveredTo, msg.Envelope.To, msg.Envelope.Cc) {
		deliveredTo = ""
	}
	ibase := assembleIMAPBase(ctx, alborz.NewBaseRenderData(ctx), mboxName, sb, railView)
	ibase.SidebarAccounts = sidebarAccounts(ctx)
	ibase.BaseRenderData.WithTitle(msg.Envelope.Subject).WithItem()
	mbox := ibase.Mailbox

	contextURL := *ctx.Request().URL
	data := &MessageRenderData{
		ContextPending:     deferContext,
		ContextURL:         template.URL(contextURL.RequestURI()),
		IMAPBaseRenderData: *ibase,
		Message:            msg,
		Part:               msg.PartByPath(partPath),
		View:               view,
		MailboxPage:        int(*mbox.NumMessages-msg.SeqNum) / messagesPerPage,
		Flags:              flags,
		NewerURL:           neighbourURL(ctx, mbox.Name(), place.newer),
		OlderURL:           neighbourURL(ctx, mbox.Name(), place.older),
		Position:           place.position,
		Total:              place.total,
		Query:              query,
		Signature:          signature,
		AuthResults:        authResults,
		Relation:           relation,
		Warnings:           warnings,
		Mark:               mark,
		InReplyTo:          inReplyTo,
		Answers:            answers,
		ThreadSupported:    threadAlgorithm != "",
		Crumb:              mailboxCrumb(sb.mailboxes, mboxName, ctx.Session.Username()),
		PreferHTML:         ctx.Reading().PreferHTML,
		Unsubscribe:        unsubscribeHref(settings, trust, msg),
		DeliveredTo:        deliveredTo,
		ForwardedBy:        ForwardedBy(msg.rootHeader, trusted, msg.ListID),
	}
	data.Invitation = messageInvitation(ctx, msg, mboxName, uid)
	return ctx.Render(http.StatusOK, "message.html", data)
}

// UnsubscribeExternal reports whether the unsubscribe link leaves
// alborz. A list's own page opens in a tab of its own; our compose form
// is this page going somewhere and should not.
func (d *MessageRenderData) UnsubscribeExternal() bool {
	return d.Unsubscribe != "" && !strings.HasPrefix(d.Unsubscribe, "/")
}

// unsubscribeHref is where the unsubscribe control should point. A list
// that gave an https page keeps it. A list that asks for a message gets
// one written from the identity it was addressed or delivered to, since
// a list keyed on an alias will not recognise the account behind it.
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
	// The request has to leave from the account the list wrote to. The
	// link names it, or compose would open on whichever account the
	// browser happens to hold as active, with that account's address.
	q.Set("account", trust.owner(msg))
	if from := writeAs(settings, trust, msg, msg.Envelope.To, msg.Envelope.Cc); from != "" {
		q.Set("from", from)
	}
	u.RawQuery = alborz.Query(q)
	return u.String()
}

// messageInvitation reads the message's scheduling part, if it has one.
// A failure here is not a failure of the page: the part is also an
// attachment, and a message that will not parse as an invitation is
// simply not shown as one.
func messageInvitation(ctx *alborz.Context, msg *IMAPMessage, mboxName string, uid imap.UID) *Invitation {
	inv, _, err := invitationAt(ctx, msg, mboxName, uid)
	if err != nil {
		ctx.Logger().Printf("failed to read the calendar part: %v", err)
		return nil
	}
	return inv
}

// PartAt reads one part of a message as the bytes it was sent as, with
// its media type. A calendar or a contact attached to a mail is filed
// from what the sender wrote, not from what a page displayed.
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

// InvitationAt reads the scheduling part of one message, and the bytes
// it was written as. The calendar plugin needs both: the fields to show
// and the object to file, which is the organizer's own and not one
// rebuilt from what a page displayed.
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
	return readInvitation(raw, part.PathString(), ctx.Session.Username()), raw, nil
}

// handleUnsubscribe takes a list at its word. RFC 8058 lets a sender
// promise that one POST to the URI in List-Unsubscribe removes the
// reader, and the POST is made from here rather than from the page: the
// browser may not post across origins, and the endpoint is the list's,
// not ours. The URI is read from the message again rather than taken
// from the form, so a page cannot ask us to post anywhere it likes.
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
	client := alborz.NewRemoteClient(unsubscribeTimeout)
	resp, err := client.Do(req)
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

// unsubscribeTimeout bounds the one request we make to somebody else's
// server on the reader's behalf.
const unsubscribeTimeout = 15 * time.Second

// sentFolder names the folder with the sent role, "" without one.
func sentFolder(mailboxes []MailboxInfo) string {
	for i := range mailboxes {
		if mailboxes[i].role() == "sent" {
			return mailboxes[i].Name()
		}
	}
	return ""
}
