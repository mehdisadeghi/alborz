package alborzbase

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-smtp"
	"github.com/labstack/echo/v4"
)

type MessageRenderData struct {
	IMAPBaseRenderData
	Message     *IMAPMessage
	Part        *IMAPPartNode
	View        interface{}
	MailboxPage int
	Flags       map[imap.Flag]bool

	// Invitation is the scheduling request this message carries, nil when
	// it carries none.
	Invitation *Invitation

	// AuthResults is what the receiving server said about the sender's
	// domain, nil when no trusted server reported or none is named.
	AuthResults *AuthResults

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
	if err := ctx.Session.DoIMAP(func(c *imapclient.Client) error {
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
	if err := ctx.DoSMTP(func(c *smtp.Client) error {
		return sendMessage(c, reply)
	}); err != nil {
		return fmt.Errorf("failed to send the answer: %w", err)
	}

	ctx.Session.PutNotice(ctx.T("invite.answered"))
	return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(
		fmt.Sprintf("/message/%s/%v", url.PathEscape(mboxName), uid))))
}

// handleDownloadMessage hands over the bytes the server holds. It is
// what makes a message filable, forwardable to somebody else's tooling,
// and checkable against what was actually stored, none of which a
// rendered page can do.
func handleDownloadMessage(ctx *alborz.Context) error {
	mboxName, uid, err := messageRef(ctx)
	if err != nil {
		return err
	}

	var raw []byte
	var env *imap.Envelope
	if err := ctx.Session.DoIMAP(func(c *imapclient.Client) error {
		var err error
		raw, env, err = fetchRawMessage(c, mboxName, uid)
		return err
	}); err != nil {
		return err
	}

	subject := ""
	if env != nil {
		subject = env.Subject
	}
	ctx.Response().Header().Set("Content-Disposition",
		downloadName(subject, fmt.Sprintf("%v", uid), ".eml"))
	return ctx.Blob(http.StatusOK, "message/rfc822", raw)
}

func handleGetPart(ctx *alborz.Context, raw bool) error {
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
	messagesPerPage := perPage(ctx, settings)

	query := ctx.QueryParam("query")
	starred := ctx.QueryParam("starred") == "1"
	var criteria *imap.SearchCriteria
	if query != "" {
		criteria = PrepareSearch(query, settings.SearchHeadersOnly)
	} else if starred {
		criteria = &imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}}
	}

	// The rendered view needs the sidebar; its mailbox LIST is issued with
	// the message's SELECT pipelined behind it, and its STATUS responses
	// ride along with the message fetch. A raw download skips the sidebar
	// entirely and only selects to fetch.
	var (
		sb                  sidebar
		msg                 *IMAPMessage
		part                *message.Entity
		selected            *imapclient.SelectedMailbox
		newerUID, olderUID  imap.UID
		position, totalMsgs int
		signature           Verification
		authResults         *AuthResults
		inReplyTo           *ThreadNeighbour
		answers             []ThreadNeighbour
		threadAlgorithm     imap.ThreadAlgorithm
	)
	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		var load *sidebarLoad
		var err error
		if !raw {
			if load, err = startSidebar(c, mboxName, mboxName, settings.Subscriptions); err != nil {
				return err
			}
		}
		if msg, part, err = getMessagePart(c, mboxName, uid, partPath); err != nil {
			return err
		}
		selected = c.Mailbox()
		if !raw {
			if newerUID, olderUID, position, totalMsgs, err = messageNeighbors(c, msg.SeqNum, criteria); err != nil {
				return err
			}
			threadAlgorithm = ThreadAlgorithm(c)
			sent := ""
			for i := range load.sb.mailboxes {
				if load.sb.mailboxes[i].role() == "sent" {
					sent = load.sb.mailboxes[i].Name()
					break
				}
			}
			// What this message answers. A server that will not search
			// headers simply shows the message alone.
			if parent, terr := threadParent(c, mboxName, sent, msg); terr == nil {
				inReplyTo = parent
			} else {
				ctx.Logger().Printf("thread lookup failed: %v", terr)
			}
			// Reading your own message, the useful direction is forward.
			if sent != "" && mboxName == sent {
				if found, aerr := threadAnswers(c, mboxName, msg); aerr == nil {
					answers = found
				} else {
					ctx.Logger().Printf("answer lookup failed: %v", aerr)
				}
			}
			// Whether the message is from who it says, on the same
			// connection that has the mailbox open. A message nobody
			// signed costs nothing here: signedParts walks a structure
			// already in hand and finds nothing.
			if msg.BodyStructure != nil {
				signature = verifySignature(c, mboxName, uid, msg.BodyStructure,
					messageRootHeader(msg), envelopeSender(msg.Envelope))
				// What our own server made of SPF, DKIM and DMARC when
				// it took delivery. Read from the same header set, and
				// only from the instance the trusted server wrote.
				authResults = readAuthResults(messageRootHeader(msg), settings.TrustedAuthServ)
			}
			sb, err = load.finish()
		}
		return err
	})
	if err != nil {
		return err
	}
	// The body fetch marked the message read, so the cached listing for
	// this folder no longer tells the truth.
	listings.evict(ctx.Session.Username(), mboxName)

	// A link naming no part, like Newer and Older, opens the part the
	// mailbox rows would link to; the bare envelope has no viewer.
	if !raw && ctx.QueryParam("part") == "" {
		preferred := msg.PreferredPart(settings.PreferHTML)
		if preferred != nil && len(preferred.Path) > 0 {
			q := ctx.Request().URL.Query()
			q.Set("part", preferred.PathString())
			return ctx.Redirect(http.StatusFound, msg.URL().String()+"?"+q.Encode())
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

	trust := newDeliveryTrust(ctx, settings, ctx.Session.Username())
	// The row says where the mail went only where To and Cc do not
	// already: an alias the sender wrote out is on the page once.
	deliveredTo := trust.alias(msg)
	if addressed(deliveredTo, msg.Envelope.To, msg.Envelope.Cc) {
		deliveredTo = ""
	}
	ibase := assembleIMAPBase(ctx, alborz.NewBaseRenderData(ctx), mboxName, sb, starred)
	ibase.SidebarAccounts = sidebarAccounts(ctx)
	ibase.BaseRenderData.WithTitle(msg.Envelope.Subject)
	mbox := ibase.Mailbox

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
		Signature:          signature,
		AuthResults:        authResults,
		Invitation:         messageInvitation(ctx, msg, mboxName, uid),
		InReplyTo:          inReplyTo,
		Answers:            answers,
		ThreadSupported:    threadAlgorithm != "",
		Crumb:              mailboxCrumb(sb.mailboxes, mboxName, ctx.Session.Username()),
		PreferHTML:         settings.PreferHTML,
		Unsubscribe:        unsubscribeHref(settings, trust, msg),
		DeliveredTo:        deliveredTo,
		ForwardedBy:        ForwardedBy(msg.rootHeader, settings.TrustedAuthServ, msg.ListID),
	})
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
	u.RawQuery = alborz.AddressQuery(q)
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
	err = ctx.Session.DoIMAP(func(c *imapclient.Client) error {
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
	if err := ctx.Session.DoIMAP(func(c *imapclient.Client) error {
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
	err := ctx.Session.DoIMAP(func(c *imapclient.Client) error {
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
		ctx.Session.PutNotice(ctx.T("notice.unsubfailed"))
		return ctx.Redirect(http.StatusFound, back)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		ctx.Logger().Printf("unsubscribe %s answered %s", msg.OneClick, resp.Status)
		ctx.Session.PutNotice(ctx.T("notice.unsubfailed"))
		return ctx.Redirect(http.StatusFound, back)
	}
	ctx.Session.PutNotice(ctx.T("notice.unsubscribed"))
	return ctx.Redirect(http.StatusFound, back)
}

// unsubscribeTimeout bounds the one request we make to somebody else's
// server on the reader's behalf.
const unsubscribeTimeout = 15 * time.Second
