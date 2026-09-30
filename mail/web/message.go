package web

import (
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type MessageRenderData struct {
	IMAPBaseRenderData
	mail.Shown
	Part *mail.IMAPPartNode
	// AsText is the HTML part read as words, offered only without a plain part.
	AsText         bool
	View           interface{}
	MailboxPage    int
	Flags          map[imap.Flag]bool
	ContextPending bool
	ContextURL     template.URL

	// AuthResults is what the receiving server said about the sender's domain.
	AuthResults *mail.AuthResults
	// Warnings are the indicators that earned a colour, and Mark the colour.
	Warnings []mail.Indicator
	Mark     mail.Grade

	// Unsubscribe is where the unsubscribe control sends the reader.
	Unsubscribe string
	// List are the list's links as the page gives them.
	List listLinks

	// InReplyTo is the message this one answers, when it is in the same folder.
	InReplyTo *mail.ThreadNeighbour

	// Answers are the messages answering this one, found only in Sent.
	Answers []mail.ThreadNeighbour

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
	return streamed(ctx, mail.StreamMessage(ctx.Request().Context(), ctx.Login, mboxName, uid, func(env *imap.Envelope, lit imap.LiteralReader) error {
		ctx.Response().Header().Set("Content-Length", fmt.Sprint(lit.Size()))
		if plain {
			// No charset can be right for a whole message whose parts each name their own.
			return ctx.Stream(http.StatusOK, "text/plain; charset=utf-8", lit)
		}
		ctx.Response().Header().Set("Content-Disposition", downloadName(env.Subject, fmt.Sprint(uid), ".eml"))
		return ctx.Stream(http.StatusOK, "message/rfc822", lit)
	}))
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

func handleDownloadAttachments(ctx *web.Context) error {
	mboxName, uid, err := MessageRef(ctx)
	if err != nil {
		return err
	}
	return streamed(ctx, mail.ZipAttachments(ctx.Request().Context(), ctx.Login, mboxName, uid, func(env *imap.Envelope) io.Writer {
		ctx.Response().Header().Set("Content-Disposition", downloadName(env.Subject, fmt.Sprint(uid), ".zip"))
		ctx.Response().Header().Set("Content-Type", "application/zip")
		ctx.Response().WriteHeader(http.StatusOK)
		return ctx.Response()
	}))
}

// handleDownloadPart streams one part, decoded, as it comes from the server.
func handleDownloadPart(ctx *web.Context, mboxName string, uid imap.UID, partPath []int) error {
	return streamed(ctx, mail.StreamPart(ctx.Request().Context(), ctx.Login, mboxName, uid, partPath, func(part *imap.BodyStructureSinglePart, name string, size int64, body io.Reader) error {
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
				"filename": name,
			}))
		}
		if size >= 0 {
			ctx.Response().Header().Set("Content-Length", fmt.Sprint(size))
		}
		return ctx.Stream(http.StatusOK, contentType, body)
	}))
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
	partPath, err := mail.ParsePartPath(ctx.QueryParam("part"))
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

	partPath, err := mail.ParsePartPath(ctx.QueryParam("part"))
	if err != nil {
		return web.NewError(http.StatusBadRequest, err)
	}

	settings, err := mail.LoadSettings(ctx.Request().Context(), ctx.Login.Store())
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
	shown, err := p.Read(ctx.Request().Context(), mail.ReadAsk{
		Login: ctx.Login, Logins: ctx.Logins(), Settings: settings,
		Mailbox: mboxName, UID: uid, Part: partPath, Named: ctx.QueryParam("part") != "",
		List: mail.ListAsk{Page: alborz.Page{Size: messagesPerPage}, Query: query, View: railView,
			Sort: ctx.QueryParam("sort"), Dir: ctx.QueryParam("dir")},
		PreferHTML: ctx.Reading().PreferHTML, Partial: ctx.Partial(), Merged: merged != nil,
		MoneyWords: strings.Split(ctx.T("indicator.moneywords"), ","),
	})
	if err != nil {
		return err
	}
	msg, partPath := shown.Message, shown.PartPath

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

	part, asText, err := mail.Body(ctx.Request().Context(), ctx.Login, msg, shown.Part, partPath, ctx.QueryParam("text") == "1")
	if err != nil {
		return err
	}

	if msg.Damaged != nil {
		ctx.Logger().Printf("message %s %d part %v: %v", mboxName, uid, partPath, msg.Damaged)
	}
	view, err := viewMessagePart(ctx, msg, partPath, part)
	if err != nil {
		view = View{}
		if err != ErrViewUnsupported {
			ctx.Logger().Printf("message viewer: %v", err)
		}
	}

	flags := make(map[imap.Flag]bool)
	for _, f := range shown.PermanentFlags {
		if f == imap.FlagWildcard {
			continue
		}
		flags[f] = msg.HasFlag(f)
	}

	ibase := assembleIMAPBase(ctx, web.NewBaseRenderData(ctx), mboxName, shown.Rail, railView)
	ibase.SidebarAccounts = p.sidebarAccounts(ctx)
	ibase.BaseRenderData.WithTitle(msg.Envelope.Subject).WithItem()
	mbox := ibase.Mailbox

	nav := neighbours(ctx, mbox.Name(), shown.Place)
	if merged != nil && !shown.DeferContext {
		if nav, err = p.mergedNeighbours(ctx, settings, merged, mboxName, uid); err != nil {
			return err
		}
	}
	contextURL := *ctx.Request().URL
	data := &MessageRenderData{
		ContextPending:     shown.DeferContext,
		ContextURL:         template.URL(contextURL.RequestURI()),
		IMAPBaseRenderData: *ibase,
		Shown:              mail.Shown{Message: msg},
		Part:               msg.PartByPath(partPath),
		AsText:             asText,
		View:               view.Body,
		MailboxPage:        int(*mbox.NumMessages-msg.SeqNum) / messagesPerPage,
		Flags:              flags,
		Neighbours:         nav,
		Query:              query,
		AuthResults:        shown.AuthResults,
		Warnings:           shown.Warnings,
		Mark:               shown.Mark,
		InReplyTo:          shown.InReplyTo,
		Answers:            shown.Answers,
		ThreadSupported:    shown.ThreadAlgorithm != "",
		Crumb:              mailboxCrumb(shown.Rail.Mailboxes, mboxName, ctx.Login.Username(), folderPath(ctx)),
		PreferHTML:         ctx.Reading().PreferHTML,
		Unsubscribe:        unsubscribeHref(ctx, shown.Unsubscribe, msg),
		List: listLinks{
			Help: listHref(ctx, msg.ListHelp), Subscribe: listHref(ctx, msg.ListSubscribe), Owner: listHref(ctx, msg.ListOwner),
		},
		DeliveredTo: shown.DeliveredTo,
		ForwardedBy: shown.ForwardedBy,

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
	return d.Unsubscribe != "" && mail.Mailto(d.Message.ListUnsubscribe) == nil
}

// unsubscribeHref is where the unsubscribe control sends the reader: the list's page, or compose with mail as its fields.
func unsubscribeHref(ctx *web.Context, mail url.Values, msg *mail.IMAPMessage) string {
	if mail != nil {
		return ctx.Path("mail.compose") + "?" + web.Query(mail)
	}
	return msg.ListUnsubscribe
}

// listLinks are where a list's links take the reader.
type listLinks struct{ Help, Subscribe, Owner string }

// A mailto opens this account's compose page, not whatever mail app the machine has.
func listHref(ctx *web.Context, uri string) string {
	if q := mail.Mailto(uri); q != nil {
		return ctx.Path("mail.compose") + "?" + web.Query(q)
	}
	return uri
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
		oneClick, err = mail.OneClick(c, mboxName, uid)
		return err
	})
	if err != nil {
		return err
	}
	back := ctx.NextOr(ctx.URL("mail.message", mboxName, uid))
	if oneClick == "" {
		return web.NewError(http.StatusBadRequest, "this message offers no one-click unsubscribe")
	}
	if err := p.Unsubscribe(ctx.Request().Context(), oneClick); err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.T("notice.unsubfailed")})
		return ctx.Redirect(http.StatusFound, back)
	}
	ctx.PutNotice(ctx.T("notice.unsubscribed"))
	return ctx.Redirect(http.StatusFound, back)
}
