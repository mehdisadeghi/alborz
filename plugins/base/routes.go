package alborzbase

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-smtp"
	"github.com/labstack/echo/v4"
	"jaytaylor.com/html2text"
)

func registerRoutes(p *alborz.GoPlugin) {
	p.GET("/", func(ctx *alborz.Context) error {
		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	})

	p.GET("/mailbox/:mbox", handleGetMailbox)
	p.POST("/mailbox/:mbox", handleGetMailbox)

	p.GET("/new-mailbox", handleNewMailbox)
	p.POST("/new-mailbox", handleNewMailbox)

	p.GET("/delete-mailbox/:mbox", handleDeleteMailbox)
	p.POST("/delete-mailbox/:mbox", handleDeleteMailbox)

	p.GET("/message/:mbox/:uid", func(ctx *alborz.Context) error {
		return handleGetPart(ctx, false)
	})
	p.GET("/message/:mbox/:uid/raw", func(ctx *alborz.Context) error {
		return handleGetPart(ctx, true)
	})

	p.GET("/login", handleLogin)
	p.POST("/login", handleLogin)
	p.POST("/switch", handleSwitch)

	p.GET("/logout", handleLogout)

	p.GET("/compose", handleComposeNew)
	p.POST("/compose", handleComposeNew)

	p.POST("/compose/attachment", handleComposeAttachment)
	p.POST("/compose/attachment/:uuid/remove", handleCancelAttachment)

	p.GET("/message/:mbox/:uid/reply", handleReply)
	p.POST("/message/:mbox/:uid/reply", handleReply)

	p.GET("/message/:mbox/:uid/forward", handleForward)
	p.POST("/message/:mbox/:uid/forward", handleForward)

	p.GET("/message/:mbox/:uid/edit", handleEdit)
	p.POST("/message/:mbox/:uid/edit", handleEdit)

	p.POST("/message/:mbox/move", handleMove)

	p.POST("/message/:mbox/delete", handleDelete)

	p.POST("/message/:mbox/flag", handleSetFlags)

	p.GET("/settings", handleSettings)
	p.POST("/settings", handleSettings)
}

type IMAPBaseRenderData struct {
	alborz.BaseRenderData
	CategorizedMailboxes CategorizedMailboxes
	Mailboxes            []MailboxInfo
	Mailbox              *MailboxStatus
	Inbox                *MailboxStatus
	Subscriptions        map[string]*MailboxStatus
	Starred              bool
}

type MailboxRenderData struct {
	IMAPBaseRenderData
	Messages                  []IMAPMessage
	PrevPage, NextPage        int
	RangeFrom, RangeTo, Total int
	Query                     string
}

type MailboxDetails struct {
	Info   *MailboxInfo
	Status *MailboxStatus
}

// Organizes mailboxes into common/uncommon categories
type CategorizedMailboxes struct {
	Common struct {
		Inbox   *MailboxDetails
		Drafts  *MailboxDetails
		Sent    *MailboxDetails
		Junk    *MailboxDetails
		Trash   *MailboxDetails
		Archive *MailboxDetails
	}
	Additional []MailboxDetails
}

func (cc *CategorizedMailboxes) Append(mi MailboxInfo, status *MailboxStatus) {
	if mi.IsInternal() {
		return
	}
	details := &MailboxDetails{
		Info:   &mi,
		Status: status,
	}
	name := mi.Mailbox
	// Check special-use attributes first (RFC 6154)
	if name == "INBOX" {
		cc.Common.Inbox = details
	} else if mi.HasAttr(string(imap.MailboxAttrDrafts)) || name == "Drafts" || name == "Draft" {
		cc.Common.Drafts = details
	} else if mi.HasAttr(string(imap.MailboxAttrSent)) || name == "Sent" {
		cc.Common.Sent = details
	} else if mi.HasAttr(string(imap.MailboxAttrJunk)) || name == "Junk" || name == "Spam" {
		cc.Common.Junk = details
	} else if mi.HasAttr(string(imap.MailboxAttrTrash)) || name == "Trash" {
		cc.Common.Trash = details
	} else if mi.HasAttr(string(imap.MailboxAttrArchive)) || name == "Archive" {
		cc.Common.Archive = details
	} else {
		cc.Additional = append(cc.Additional, *details)
	}
}

func newIMAPBaseRenderData(ctx *alborz.Context,
	base *alborz.BaseRenderData) (*IMAPBaseRenderData, error) {

	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err)
	}

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return nil, fmt.Errorf("failed to load settings: %v", err)
	}

	statuses := make(map[string]*MailboxStatus)
	var mailboxes []MailboxInfo
	var active, inbox *MailboxStatus
	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		var err error
		if mailboxes, err = listMailboxes(c); err != nil {
			return err
		}

		if c.Caps().Has(imap.CapListStatus) {
			for _, mbox := range mailboxes {
				if mbox.Status == nil {
					continue
				}
				statuses[mbox.Name()] = &MailboxStatus{mbox.Status}
			}
			inbox = statuses["INBOX"]
			active = statuses[mboxName]
			return nil
		}

		if mboxName != "" {
			if active, err = getMailboxStatus(c, mboxName); err != nil {
				return echo.NewHTTPError(http.StatusNotFound, err)
			}
		}

		if mboxName == "INBOX" {
			inbox = active
		} else {
			if inbox, err = getMailboxStatus(c, "INBOX"); err != nil {
				return err
			}
		}

		for _, sub := range settings.Subscriptions {
			if status, err := getMailboxStatus(c, sub); err != nil {
				return err
			} else {
				statuses[sub] = status
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if mboxName != "" {
		statuses[mboxName] = active
	}
	statuses["INBOX"] = inbox

	// The starred view filters the active mailbox; highlight the Starred
	// sidebar entry instead of the mailbox itself.
	starred := ctx.QueryParam("starred") == "1"

	var categorized CategorizedMailboxes
	for i := range mailboxes {
		// Populate unseen & active states
		if active != nil && mailboxes[i].Name() == active.Mailbox && !starred {
			mailboxes[i].Active = true
		}
		status := statuses[mailboxes[i].Name()]
		if status != nil {
			mailboxes[i].Unseen = int(*status.NumUnseen)
			mailboxes[i].Total = int(*status.NumMessages)
		}

		categorized.Append(mailboxes[i], status)
	}

	return &IMAPBaseRenderData{
		BaseRenderData:       *base,
		CategorizedMailboxes: categorized,
		Mailboxes:            mailboxes,
		Inbox:                inbox,
		Mailbox:              active,
		Subscriptions:        statuses,
		Starred:              starred,
	}, nil
}

func handleGetMailbox(ctx *alborz.Context) error {
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	mbox := ibase.Mailbox
	title := mbox.Name()
	if title == "INBOX" {
		title = "Inbox"
	}
	if ibase.Starred {
		title = "Starred"
	} else if *mbox.NumUnseen > 0 {
		title = fmt.Sprintf("(%d) %s", *mbox.NumUnseen, title)
	}
	ibase.BaseRenderData.WithTitle(title)

	page := 0
	if pageStr := ctx.QueryParam("page"); pageStr != "" {
		var err error
		if page, err = strconv.Atoi(pageStr); err != nil || page < 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid page index")
		}
	}

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	messagesPerPage := settings.MessagesPerPage

	query := ctx.QueryParam("query")

	var (
		msgs  []IMAPMessage
		total int
	)
	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		var err error
		switch {
		case query != "":
			msgs, total, err = searchMessages(c, mbox.Name(), PrepareSearch(query), page, messagesPerPage)
		case ibase.Starred:
			criteria := &imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}}
			msgs, total, err = searchMessages(c, mbox.Name(), criteria, page, messagesPerPage)
		default:
			msgs, total, err = listMessages(c, mbox.Name(), page, messagesPerPage)
		}
		return err
	})
	if err != nil {
		return err
	}

	prevPage, nextPage := -1, -1
	if page > 0 {
		prevPage = page - 1
	}
	if (page+1)*messagesPerPage < total {
		nextPage = page + 1
	}

	rangeFrom, rangeTo := 0, 0
	if len(msgs) > 0 {
		rangeFrom = page*messagesPerPage + 1
		rangeTo = page*messagesPerPage + len(msgs)
	}

	return ctx.Render(http.StatusOK, "mailbox.html", &MailboxRenderData{
		IMAPBaseRenderData: *ibase,
		Messages:           msgs,
		PrevPage:           prevPage,
		NextPage:           nextPage,
		RangeFrom:          rangeFrom,
		RangeTo:            rangeTo,
		Total:              total,
		Query:              query,
	})
}

type NewMailboxRenderData struct {
	IMAPBaseRenderData
	Error string
}

func handleNewMailbox(ctx *alborz.Context) error {
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	ibase.BaseRenderData.WithTitle("Create new folder")

	if ctx.Request().Method == http.MethodPost {
		name := ctx.FormValue("name")
		if name == "" {
			return ctx.Render(http.StatusOK, "new-mailbox.html", &NewMailboxRenderData{
				IMAPBaseRenderData: *ibase,
				Error:              "Name is required",
			})
		}

		err := ctx.Session.DoIMAP(func(c *imapclient.Client) error {
			return c.Create(name, nil).Wait()
		})

		if err != nil {
			return ctx.Render(http.StatusOK, "new-mailbox.html", &NewMailboxRenderData{
				IMAPBaseRenderData: *ibase,
				Error:              err.Error(),
			})
		}

		return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%s", url.PathEscape(name)))
	}

	return ctx.Render(http.StatusOK, "new-mailbox.html", &NewMailboxRenderData{
		IMAPBaseRenderData: *ibase,
		Error:              "",
	})
}

func handleDeleteMailbox(ctx *alborz.Context) error {
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	mbox := ibase.Mailbox
	ibase.BaseRenderData.WithTitle("Delete folder '" + mbox.Name() + "'")

	if ctx.Request().Method == http.MethodPost {
		ctx.Session.DoIMAP(func(c *imapclient.Client) error {
			return c.Delete(mbox.Name()).Wait()
		})
		ctx.Session.PutNotice("Mailbox deleted.")
		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	}

	return ctx.Render(http.StatusOK, "delete-mailbox.html", ibase)
}

func handleLogin(ctx *alborz.Context) error {
	username := ctx.FormValue("username")
	password := ctx.FormValue("password")
	remember := ctx.FormValue("remember-me")
	add := ctx.QueryParam("add") == "1"

	renderData := struct {
		alborz.BaseRenderData
		CanRememberMe bool
		Add           bool
	}{
		BaseRenderData: *alborz.NewBaseRenderData(ctx),
		CanRememberMe:  ctx.Server.Options.LoginKey != nil,
		Add:            add,
	}

	// The remembered credentials would re-login the accounts the user
	// already has, not add a new one.
	if username == "" && password == "" && !add && ctx.RestoreRememberedAccounts() {
		return loginRedirect(ctx)
	}

	if username != "" && password != "" {
		s, err := ctx.Server.Sessions.Put(username, password)
		if err != nil {
			ctx.Logger().Printf("Login failed for %q: %v", username, err)
			if _, ok := err.(alborz.AuthError); ok {
				renderData.BaseRenderData.GlobalData.Notice = "Failed to login!"
				return ctx.Render(http.StatusUnauthorized, "login.html", &renderData)
			}
			var domainErr alborz.UnknownDomainError
			if errors.As(err, &domainErr) {
				renderData.BaseRenderData.GlobalData.Notice = "Failed to login: " + domainErr.Error()
				return ctx.Render(http.StatusUnauthorized, "login.html", &renderData)
			}
			var netErr *net.OpError
			if errors.As(err, &netErr) {
				renderData.BaseRenderData.GlobalData.Notice = fmt.Sprintf("Failed to login: %v", netErr.Err)
				return ctx.Render(http.StatusServiceUnavailable, "login.html", &renderData)
			}
			return fmt.Errorf("failed to put connection in pool: %v", err)
		}
		ctx.AddAccount(s)

		if remember == "on" {
			ctx.SetLoginToken(username, password)
		}

		return loginRedirect(ctx)
	}

	return ctx.Render(http.StatusOK, "login.html", &renderData)
}

// loginRedirect honors the next parameter after a successful login.
func loginRedirect(ctx *alborz.Context) error {
	// A second leading slash or backslash would make the target
	// scheme-relative, redirecting off-site.
	if path := ctx.QueryParam("next"); path != "" && path[0] == '/' && path != "/login" &&
		!strings.HasPrefix(path, "//") && !strings.HasPrefix(path, "/\\") {
		return ctx.Redirect(http.StatusFound, path)
	}
	return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
}

func handleLogout(ctx *alborz.Context) error {
	if next := ctx.Logout(); next != nil {
		next.PutNotice("Signed in as " + next.Username() + ".")
		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	}
	return ctx.Redirect(http.StatusFound, "/login")
}

func handleSwitch(ctx *alborz.Context) error {
	if !ctx.SwitchAccount(ctx.FormValue("account")) {
		ctx.Session.PutNotice("That session has expired, sign in again.")
		return ctx.Redirect(http.StatusFound, "/login?add=1")
	}
	return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
}

type MessageRenderData struct {
	IMAPBaseRenderData
	Message     *IMAPMessage
	Part        *IMAPPartNode
	View        interface{}
	MailboxPage int
	Flags       map[imap.Flag]bool

	// Neighbors in the view the message was opened from; nil when absent.
	NewerURL *url.URL
	OlderURL *url.URL
	Position int
	Total    int
	Query    string
}

func handleGetPart(ctx *alborz.Context, raw bool) error {
	_, uid, err := parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return err
	}
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}
	mbox := ibase.Mailbox

	partPath, err := parsePartPath(ctx.QueryParam("part"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	messagesPerPage := settings.MessagesPerPage

	query := ctx.QueryParam("query")
	var criteria *imap.SearchCriteria
	if query != "" {
		criteria = PrepareSearch(query)
	} else if ibase.Starred {
		criteria = &imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}}
	}

	var msg *IMAPMessage
	var part *message.Entity
	var selected *imapclient.SelectedMailbox
	var newerUID, olderUID imap.UID
	var position, totalMsgs int
	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		var err error
		if msg, part, err = getMessagePart(c, mbox.Name(), uid, partPath); err != nil {
			return err
		}
		selected = c.Mailbox()
		if !raw {
			newerUID, olderUID, position, totalMsgs, err = messageNeighbors(c, msg.SeqNum, criteria)
		}
		return err
	})
	if err != nil {
		return err
	}

	// A link naming no part, like Newer and Older, opens the part the
	// mailbox rows would link to; the bare envelope has no viewer.
	if !raw && ctx.QueryParam("part") == "" {
		preferred := msg.TextPart()
		if preferred == nil {
			preferred = msg.HTMLPart()
		}
		if preferred != nil && len(preferred.Path) > 0 {
			q := ctx.Request().URL.Query()
			q.Set("part", preferred.PathString())
			return ctx.Redirect(http.StatusFound, msg.URL().String()+"?"+q.Encode())
		}
	}

	mimeType, _, err := part.Header.ContentType()
	if err != nil {
		return fmt.Errorf("failed to parse part Content-Type: %v", err)
	}
	if len(partPath) == 0 {
		if ctx.QueryParam("plain") == "1" {
			mimeType = "text/plain"
		} else {
			mimeType = "message/rfc822"
		}
	}

	if raw {
		ctx.Response().Header().Set("Content-Type", mimeType)

		disp, dispParams, _ := part.Header.ContentDisposition()
		filename := dispParams["filename"]
		if len(partPath) == 0 {
			filename = msg.Envelope.Subject + ".eml"
		}

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

		if len(partPath) == 0 {
			return part.WriteTo(ctx.Response())
		}
		return ctx.Stream(http.StatusOK, mimeType, part.Body)
	}

	view, err := viewMessagePart(ctx, msg, part)
	if err == ErrViewUnsupported {
		view = nil
	}

	flags := make(map[imap.Flag]bool)
	for _, f := range selected.PermanentFlags {
		if f == imap.FlagWildcard {
			continue
		}
		flags[f] = msg.HasFlag(f)
	}

	ibase.BaseRenderData.WithTitle(msg.Envelope.Subject)

	return ctx.Render(http.StatusOK, "message.html", &MessageRenderData{
		IMAPBaseRenderData: *ibase,
		Message:            msg,
		Part:               msg.PartByPath(partPath),
		View:               view,
		MailboxPage:        int(*mbox.NumMessages-msg.SeqNum) / messagesPerPage,
		Flags:              flags,
		NewerURL:           messageURL(mbox.Name(), newerUID),
		OlderURL:           messageURL(mbox.Name(), olderUID),
		Position:           position,
		Total:              totalMsgs,
		Query:              query,
	})
}

type ComposeRenderData struct {
	IMAPBaseRenderData
	Message *OutgoingMessage
}

type messagePath struct {
	Mailbox string
	Uid     imap.UID
}

type composeOptions struct {
	Draft     *messagePath
	Forward   *messagePath
	InReplyTo *messagePath
}

// Send message, append it to the Sent mailbox, mark the original message as
// answered
func submitCompose(ctx *alborz.Context, msg *OutgoingMessage, options *composeOptions) error {
	err := ctx.Session.DoSMTP(func(c *smtp.Client) error {
		return sendMessage(c, msg)
	})
	if err != nil {
		if _, ok := err.(alborz.AuthError); ok {
			return echo.NewHTTPError(http.StatusForbidden, err)
		}
		return fmt.Errorf("failed to send message: %v", err)
	}

	if inReplyTo := options.InReplyTo; inReplyTo != nil {
		err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
			return markMessageAnswered(c, inReplyTo.Mailbox, inReplyTo.Uid)
		})
		if err != nil {
			return fmt.Errorf("failed to mark original message as answered: %v", err)
		}
	}

	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		if _, err := appendMessage(c, msg, mailboxSent); err != nil {
			return err
		}
		if draft := options.Draft; draft != nil {
			if err := deleteMessage(c, draft.Mailbox, draft.Uid); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to save message to Sent mailbox: %v", err)
	}

	ctx.Session.PutNotice("Message sent.")
	return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
}

func handleCompose(ctx *alborz.Context, msg *OutgoingMessage, options *composeOptions) error {
	ibase, err := newIMAPBaseRenderData(ctx, alborz.NewBaseRenderData(ctx))
	if err != nil {
		return err
	}

	if msg.From == "" && strings.ContainsRune(ctx.Session.Username(), '@') {
		settings, err := LoadSettings(ctx.Session.Store())
		if err != nil {
			return err
		}
		if settings.From != "" {
			addr := mail.Address{
				Name:    settings.From,
				Address: ctx.Session.Username(),
			}
			msg.From = addr.String()
		} else {
			msg.From = ctx.Session.Username()
		}
	}

	if ctx.Request().Method == http.MethodPost {
		formParams, err := ctx.FormParams()
		if err != nil {
			return fmt.Errorf("failed to parse form: %v", err)
		}
		_, saveAsDraft := formParams["save_as_draft"]

		msg.From = ctx.FormValue("from")
		msg.To = parseAddressList(ctx.FormValue("to"))
		msg.Cc = parseAddressList(ctx.FormValue("cc"))
		msg.Bcc = parseAddressList(ctx.FormValue("bcc"))
		msg.Subject = ctx.FormValue("subject")
		msg.Text = ctx.FormValue("text")
		msg.InReplyTo = ctx.FormValue("in_reply_to")
		msg.MessageID = ctx.FormValue("message_id")

		form, err := ctx.MultipartForm()
		if err != nil {
			return fmt.Errorf("failed to get multipart form: %v", err)
		}

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
				err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
					var err error
					_, part, err = getMessagePart(c, original.Mailbox, original.Uid, path)
					return err
				})
				if err != nil {
					return fmt.Errorf("failed to fetch attachment from original message: %v", err)
				}

				var buf bytes.Buffer
				if _, err := io.Copy(&buf, part.Body); err != nil {
					return fmt.Errorf("failed to copy attachment from original message: %v", err)
				}

				h := mail.AttachmentHeader{part.Header}
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

		if saveAsDraft {
			var (
				drafts *MailboxInfo
				uid    imap.UID
			)
			err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
				drafts, err = appendMessage(c, msg, mailboxDrafts)
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
				} else if uids := data.AllUIDs(); len(uids) != 1 {
					panic(fmt.Errorf("Duplicate message ID"))
				} else {
					uid = uids[0]
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("failed to save message to Draft mailbox: %v", err)
			}
			ctx.Session.PutNotice("Message saved as draft.")
			return ctx.Redirect(http.StatusFound, fmt.Sprintf(
				"/message/%s/%d/edit?part=1", drafts.Mailbox, uid))
		} else {
			return submitCompose(ctx, msg, options)
		}
	}

	return ctx.Render(http.StatusOK, "compose.html", &ComposeRenderData{
		IMAPBaseRenderData: *ibase,
		Message:            msg,
	})
}

func handleComposeNew(ctx *alborz.Context) error {
	text := ctx.QueryParam("body")
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return nil
	}
	if text == "" && settings.Signature != "" {
		text = "\n\n\n-- \n" + settings.Signature
	}

	// These are common mailto URL query parameters
	var hdr mail.Header
	hdr.GenerateMessageID()
	mid, _ := hdr.MessageID()
	return handleCompose(ctx, &OutgoingMessage{
		To:        strings.Split(ctx.QueryParam("to"), ","),
		Subject:   ctx.QueryParam("subject"),
		MessageID: "<" + mid + ">",
		InReplyTo: ctx.QueryParam("in-reply-to"),
		Text:      text,
	}, &composeOptions{})
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
	inReplyToPath.Mailbox, inReplyToPath.Uid, err = parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
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

func populateMessageFromOriginalMessage(ctx *alborz.Context, inReplyToPath messagePath) (OutgoingMessage, error) {
	var ret OutgoingMessage

	partPath, err := parsePartPath(ctx.QueryParam("part"))
	if err != nil {
		return ret, echo.NewHTTPError(http.StatusBadRequest, err)
	}

	var inReplyTo *IMAPMessage
	var part *message.Entity
	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		var err error
		inReplyTo, part, err = getMessagePart(c, inReplyToPath.Mailbox,
			inReplyToPath.Uid, partPath)
		return err
	})
	if err != nil {
		return ret, err
	}

	mimeType, _, err := part.Header.ContentType()
	if err != nil {
		return ret, fmt.Errorf("failed to parse part Content-Type: %v", err)
	}

	switch mimeType {
	case "text/plain":
		ret.Text, err = quote(part.Body)
	case "text/html":
		var text string
		text, err = html2text.FromReader(part.Body, html2text.Options{})
		if err != nil {
			return ret, err
		}

		ret.Text, err = quote(strings.NewReader(text))
	default:
		defErr := fmt.Errorf("cannot forward %q part", mimeType)
		err = echo.NewHTTPError(http.StatusBadRequest, defErr)
	}
	if err != nil {
		return ret, err
	}

	var hdr mail.Header
	hdr.GenerateMessageID()
	mid, _ := hdr.MessageID()
	ret.MessageID = "<" + mid + ">"
	ret.InReplyTo = "<" + inReplyTo.Envelope.MessageID + ">"
	// TODO: populate From from known user addresses and inReplyTo.Envelope.To
	replyTo := inReplyTo.Envelope.ReplyTo
	if len(replyTo) == 0 {
		replyTo = inReplyTo.Envelope.From
	}
	ret.To = unwrapIMAPAddressList(replyTo)

	if ctx.QueryParam("all") != "" {
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

func filterOutUsername(username string, addresses []imap.Address) []imap.Address {
	for i, addr := range addresses {
		if addr.Addr() == username {
			return append(addresses[:i], addresses[i+1:]...)
		}
	}
	return addresses
}

func handleForward(ctx *alborz.Context) error {
	var sourcePath messagePath
	var err error
	sourcePath.Mailbox, sourcePath.Uid, err = parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		// Populate fields from original message
		partPath, err := parsePartPath(ctx.QueryParam("part"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		var source *IMAPMessage
		var part *message.Entity
		err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
			var err error
			source, part, err = getMessagePart(c, sourcePath.Mailbox, sourcePath.Uid, partPath)
			return err
		})
		if err != nil {
			return err
		}

		mimeType, _, err := part.Header.ContentType()
		if err != nil {
			return fmt.Errorf("failed to parse part Content-Type: %v", err)
		}

		switch mimeType {
		case "text/plain":
			msg.Text, err = quote(part.Body)
		case "text/html":
			var text string
			text, err = html2text.FromReader(part.Body, html2text.Options{})
			if err != nil {
				return err
			}

			msg.Text, err = quote(strings.NewReader(text))
		default:
			defErr := fmt.Errorf("cannot forward %q part", mimeType)
			err = echo.NewHTTPError(http.StatusBadRequest, defErr)
		}
		if err != nil {
			return err
		}

		var hdr mail.Header
		hdr.GenerateMessageID()
		mid, _ := hdr.MessageID()
		msg.MessageID = "<" + mid + ">"
		msg.Subject = source.Envelope.Subject
		if !strings.HasPrefix(strings.ToLower(msg.Subject), "fwd:") &&
			!strings.HasPrefix(strings.ToLower(msg.Subject), "fw:") {
			msg.Subject = "Fwd: " + msg.Subject
		}
		msg.InReplyTo = formatMsgIDList(source.Envelope.InReplyTo)

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
	sourcePath.Mailbox, sourcePath.Uid, err = parseMboxAndUid(ctx.Param("mbox"), ctx.Param("uid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	// TODO: somehow get the path to the In-Reply-To message (with a search?)

	var msg OutgoingMessage
	if ctx.Request().Method == http.MethodGet {
		// Populate fields from source message
		partPath, err := parsePartPath(ctx.QueryParam("part"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		var source *IMAPMessage
		var part *message.Entity
		err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
			var err error
			source, part, err = getMessagePart(c, sourcePath.Mailbox, sourcePath.Uid, partPath)
			return err
		})
		if err != nil {
			return err
		}

		mimeType, _, err := part.Header.ContentType()
		if err != nil {
			return fmt.Errorf("failed to parse part Content-Type: %v", err)
		}

		if !strings.EqualFold(mimeType, "text/plain") {
			err := fmt.Errorf("cannot edit %q part", mimeType)
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}

		b, err := ioutil.ReadAll(part.Body)
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

// The explicit query parameter a button carries wins over a form field,
// so the bulk move selector cannot leak into other actions.
func formOrQueryParam(ctx *alborz.Context, k string) string {
	if v := ctx.QueryParam(k); v != "" {
		return v
	}
	return ctx.FormValue(k)
}

func handleMove(ctx *alborz.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	formParams, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	uids, err := parseUidList(formParams["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	if len(uids) == 0 {
		ctx.Session.PutNotice("No messages selected.")
		return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))
	}

	to := formOrQueryParam(ctx, "to")
	if to == "" {
		ctx.Session.PutNotice("No destination folder selected.")
		return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))
	}

	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}

		if _, err := c.Move(imap.UIDSetNum(uids...), to).Wait(); err != nil {
			return fmt.Errorf("failed to move message: %v", err)
		}

		// TODO: get the UID of the message in the destination mailbox with UIDPLUS
		return nil
	})
	if err != nil {
		return err
	}

	ctx.Session.PutNotice("Message(s) moved.")
	if path := formOrQueryParam(ctx, "next"); path != "" {
		return ctx.Redirect(http.StatusFound, path)
	}
	return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))
}

func handleDelete(ctx *alborz.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	formParams, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}
	uids, err := parseUidList(formParams["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	if len(uids) == 0 {
		ctx.Session.PutNotice("No messages selected.")
		return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))
	}

	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}

		err := c.Store(imap.UIDSetNum(uids...), &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Silent: true,
			Flags:  []imap.Flag{imap.FlagDeleted},
		}, nil).Close()
		if err != nil {
			return fmt.Errorf("failed to add deleted flag: %v", err)
		}

		if err := c.Expunge().Close(); err != nil {
			return fmt.Errorf("failed to expunge mailbox: %v", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	ctx.Session.PutNotice("Message(s) deleted.")
	if path := formOrQueryParam(ctx, "next"); path != "" {
		return ctx.Redirect(http.StatusFound, path)
	}
	return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))
}

func handleSetFlags(ctx *alborz.Context) error {
	mboxName, err := url.PathUnescape(ctx.Param("mbox"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	formParams, err := ctx.FormParams()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	uids, err := parseUidList(formParams["uids"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	flags, ok := formParams["flags"]
	if !ok {
		flagsStr := ctx.QueryParam("to")
		if flagsStr == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "missing 'flags' form parameter")
		}
		flags = strings.Fields(flagsStr)
	}

	actionStr := ctx.FormValue("action")
	if actionStr == "" {
		actionStr = ctx.QueryParam("action")
	}

	var op imap.StoreFlagsOp
	switch actionStr {
	case "", "set":
		op = imap.StoreFlagsSet
	case "add":
		op = imap.StoreFlagsAdd
	case "remove":
		op = imap.StoreFlagsDel
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "invalid 'action' value")
	}

	l := make([]imap.Flag, len(flags))
	for i, s := range flags {
		l[i] = imap.Flag(s)
	}

	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mboxName); err != nil {
			return err
		}

		err := c.Store(imap.UIDSetNum(uids...), &imap.StoreFlags{
			Op:     op,
			Silent: true,
			Flags:  l,
		}, nil).Close()
		if err != nil {
			return fmt.Errorf("failed to set flags: %v", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	if path := formOrQueryParam(ctx, "next"); path != "" {
		return ctx.Redirect(http.StatusFound, path)
	}
	if len(uids) != 1 || (op == imap.StoreFlagsDel && len(l) == 1 && l[0] == imap.FlagSeen) {
		// Redirecting to the message view would mark the message as read again
		return ctx.Redirect(http.StatusFound, fmt.Sprintf("/mailbox/%v", url.PathEscape(mboxName)))
	}
	return ctx.Redirect(http.StatusFound, fmt.Sprintf("/message/%v/%v", url.PathEscape(mboxName), uids[0]))
}

const settingsKey = "base.settings"
const maxMessagesPerPage = 100

type Settings struct {
	MessagesPerPage int
	Signature       string
	From            string
	Subscriptions   []string
	Timezone        string
	FirstDayOfWeek  int    // 0 = Sunday, 1 = Monday (default)
}

func LoadSettings(s alborz.Store) (*Settings, error) {
	settings := &Settings{
		MessagesPerPage: 50,
		FirstDayOfWeek:  1, // Monday
	}
	if err := s.Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	if err := settings.check(); err != nil {
		return nil, err
	}
	return settings, nil
}

func (s *Settings) check() error {
	if s.MessagesPerPage <= 0 || s.MessagesPerPage > maxMessagesPerPage {
		return fmt.Errorf("messages per page out of bounds: %v", s.MessagesPerPage)
	}
	if len(s.Signature) > 2048 {
		return fmt.Errorf("Signature must be 2048 characters or fewer")
	}
	if len(s.From) > 512 {
		return fmt.Errorf("Full name must be 512 characters or fewer")
	}
	return nil
}

type SettingsRenderData struct {
	alborz.BaseRenderData
	Mailboxes     []MailboxInfo
	Settings      *Settings
	Subscriptions Subscriptions
	ColorScheme   string // per-user browser preference
	Theme         string
}

type Subscriptions []string

func (s Subscriptions) Has(sub string) bool {
	for _, cand := range s {
		if cand == sub {
			return true
		}
	}
	return false
}

func handleSettings(ctx *alborz.Context) error {
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return fmt.Errorf("failed to load settings: %v", err)
	}

	var mailboxes []MailboxInfo
	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		mailboxes, err = listMailboxes(c)
		return err
	})
	if err != nil {
		return err
	}

	if ctx.Request().Method == http.MethodPost {
		settings.MessagesPerPage, err = strconv.Atoi(ctx.FormValue("messages_per_page"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid messages per page: %v", err)
		}
		settings.Signature = ctx.FormValue("signature")
		settings.From = ctx.FormValue("from")
		settings.Timezone = ctx.FormValue("timezone")
		ctx.SetColorScheme(ctx.FormValue("color_scheme"))
		ctx.SetTheme(ctx.FormValue("theme"))
		if fdow := ctx.FormValue("first_day_of_week"); fdow != "" {
			settings.FirstDayOfWeek, err = strconv.Atoi(fdow)
			if err != nil || settings.FirstDayOfWeek < 0 || settings.FirstDayOfWeek > 6 {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid first day of week")
			}
		}

		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		settings.Subscriptions = params["subscriptions"]

		if err := settings.check(); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err)
		}
		if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
			return fmt.Errorf("failed to save settings: %v", err)
		}

		return ctx.Redirect(http.StatusFound, "/mailbox/INBOX")
	}

	return ctx.Render(http.StatusOK, "settings.html", &SettingsRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx),
		Settings:       settings,
		Mailboxes:      mailboxes,
		Subscriptions:  Subscriptions(settings.Subscriptions),
		ColorScheme:    ctx.ColorScheme(),
		Theme:          ctx.Theme(),
	})
}
