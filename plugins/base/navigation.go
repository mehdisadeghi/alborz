package alborzbase

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/textproto"
)

// listPlace is where a message stands in the list it was opened from:
// the rows either side of it, its position and the list's total.
type listPlace struct {
	newer, older    imap.UID
	position, total int
}

// cachedPlace is the cached page of the list the message was opened
// from, nil when none is held, and the message's place in it. That
// page is the list the reader came from, narrowing and order and all,
// so its neighbours are the right ones; placed is false where the
// server has to say.
func cachedPlace(ctx *alborz.Context, mailbox string, uid imap.UID, query, view string) (cached *listingEntry, place listPlace, placed bool) {
	sortKey, _ := listOrder(ctx)
	return listings.message(ctx.Session.Username(), listingView(mailbox, query, view, sortKey, ctx.QueryParam("dir")), uid, PerPage(ctx))
}

// placeInList asks the server for the same. The order the list was in
// is the order Newer and Older mean: the message was opened from that
// list, and its links carry it on.
func placeInList(ctx *alborz.Context, c *imapclient.Client, settings *Settings, mailbox string, uid imap.UID, query, view string) (place listPlace, err error) {
	if err := ensureMailboxSelected(c, mailbox); err != nil {
		return place, err
	}
	sortKey, reverse := listOrder(ctx)
	place.newer, place.older, place.position, place.total, err = messageNeighbors(c, uid, listCriteria(c, settings, query, view), sortKey, reverse)
	return place, err
}

// readThread is what the message answers and, for the reader's own in
// Sent, where the useful direction is forward, what answered it. A
// server that will not search headers simply shows the message alone.
func readThread(ctx *alborz.Context, c *imapclient.Client, mailbox, sent string, msg *IMAPMessage) (*ThreadNeighbour, []ThreadNeighbour) {
	parent, err := threadParent(c, mailbox, sent, msg)
	if err != nil {
		ctx.Logger().Printf("thread lookup failed: %v", err)
	}
	if sent == "" || mailbox != sent {
		return parent, nil
	}
	answers, err := threadAnswers(c, mailbox, msg)
	if err != nil {
		ctx.Logger().Printf("answer lookup failed: %v", err)
	}
	return parent, answers
}

// Navigation never downloads a body or changes the read state.
func handleMessageNavigation(ctx *alborz.Context) error {
	mailbox, uid, err := messageRef(ctx)
	if err != nil {
		return err
	}
	view, err := readView(ctx)
	if err != nil {
		return err
	}
	sb, err := sidebarFor(ctx.Session)
	if err != nil {
		return err
	}
	sb = sb.clone()
	sb.active = sb.statuses[mailbox]
	data := &MessageRenderData{Query: ctx.QueryParam("query")}
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		return err
	}
	cached, place, placed := cachedPlace(ctx, mailbox, uid, data.Query, view)
	if cached != nil {
		data.Message = cached.row(uid)
		data.ThreadSupported = cached.threadAlgorithm != ""
	}

	err = ctx.DoIMAP(func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mailbox); err != nil {
			return err
		}
		if data.Message == nil {
			header := &imap.FetchItemBodySection{Peek: true, Specifier: imap.PartSpecifierHeader,
				HeaderFields: []string{"References", "List-Id"}}
			msgs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, Envelope: true,
				BodySection: []*imap.FetchItemBodySection{header}}).Collect()
			if err != nil {
				return err
			}
			if len(msgs) == 0 {
				return alborz.NotFound("notfound.message", fmt.Sprint(uid))
			}
			msg := &IMAPMessage{FetchMessageBuffer: msgs[0], Mailbox: mailbox}
			if h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(msgs[0].FindBodySection(header)))); err == nil {
				msg.setListHeaders(h)
			}
			data.Message = msg
		}
		msg := data.Message
		if !placed {
			var err error
			if place, err = placeInList(ctx, c, settings, mailbox, uid, data.Query, view); err != nil {
				return err
			}
		}
		data.ThreadSupported = ThreadAlgorithm(c) != ""
		data.InReplyTo, data.Answers = readThread(ctx, c, mailbox, sentFolder(sb.mailboxes), msg)
		if sb.active == nil {
			st, err := c.Status(mailbox, listingStatusOptions(c)).Wait()
			if err != nil {
				return err
			}
			sb.active = &MailboxStatus{StatusData: st}
		}
		return nil
	})
	if err != nil {
		return err
	}
	data.NewerURL, data.OlderURL = neighbourURL(ctx, mailbox, place.newer), neighbourURL(ctx, mailbox, place.older)
	data.Position, data.Total = place.position, place.total
	data.IMAPBaseRenderData = *assembleIMAPBase(ctx, alborz.NewBaseRenderData(ctx), mailbox, sb, view)
	return ctx.Render(http.StatusOK, "message-context", data)
}
