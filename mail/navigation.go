package mail

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/search"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/textproto"
)

type listPlace struct {
	newer, older    imap.UID
	position, total int
}

// messageList is the order a message's list is cut from, and whether that is the folder's own.
func messageList(ctx *alborz.Context, mailbox, query, view string) (order string, own bool) {
	if ctx.QueryParam("sort") == threadSort {
		return orderView(mailbox, query, view, threadSort, true), false
	}
	sortKey, reverse := listOrder(ctx)
	return orderView(mailbox, query, view, sortKey, reverse), query == "" && view == "" && sortKey == "" && reverse
}

// A narrowed, sorted or threaded list places the message in its held order; the folder's own, in its held pages.
func (p *plugin) cachedPlace(ctx *alborz.Context, mailbox string, uid imap.UID, query, view string) (cached *listingEntry, place listPlace, placed bool) {
	user := ctx.Login.Username()
	sortKey, _ := listOrder(ctx)
	cached, place, placed = p.listings.message(user, listingView(mailbox, query, view, sortKey, ctx.QueryParam("dir")), uid, alborz.PerPage(ctx))
	if order, own := messageList(ctx, mailbox, query, view); !own {
		if o, ok := p.listings.heldOrder(user, order); ok {
			return cached, placeIn(o.uids, uid), true
		}
	}
	return cached, place, placed
}

func (p *plugin) placeInList(ctx *alborz.Context, c *imapclient.Client, settings *Settings, mailbox string, uid imap.UID, query, view string) (listPlace, error) {
	if err := ensureMailboxSelected(c, mailbox); err != nil {
		return listPlace{}, err
	}
	order, own := messageList(ctx, mailbox, query, view)
	algorithm := ThreadAlgorithm(c)
	threaded := ctx.QueryParam("sort") == threadSort
	if own || threaded && algorithm == "" {
		return folderNeighbors(c, uid)
	}
	o, err := p.listings.order(ctx.Login.Username(), order, func() (messageOrder, error) {
		criteria := listCriteria(c, settings, query, view)
		if threaded {
			return threadOrder(c, algorithm, criteria)
		}
		sortKey, reverse := listOrder(ctx)
		uids, err := viewOrder(c, search.Parse(query), criteria, sortKey, reverse)
		return messageOrder{uids: uids}, err
	})
	if err != nil {
		return listPlace{}, err
	}
	return placeIn(o.uids, uid), nil
}

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

func (p *plugin) handleMessageNavigation(ctx *alborz.Context) error {
	mailbox, uid, err := messageRef(ctx)
	if err != nil {
		return err
	}
	view, err := readView(ctx)
	if err != nil {
		return err
	}
	sb, err := p.sidebarFor(ctx.Login)
	if err != nil {
		return err
	}
	sb = sb.clone()
	sb.active = sb.statuses[mailbox]
	data := &MessageRenderData{Query: ctx.QueryParam("query")}
	settings, err := LoadSettings(ctx.Request().Context(), ctx.Login.Store())
	if err != nil {
		return err
	}
	cached, place, placed := p.cachedPlace(ctx, mailbox, uid, data.Query, view)
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
			if place, err = p.placeInList(ctx, c, settings, mailbox, uid, data.Query, view); err != nil {
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
	data.Neighbours = neighbours(ctx, mailbox, place)
	data.IMAPBaseRenderData = *assembleIMAPBase(ctx, alborz.NewBaseRenderData(ctx), mailbox, sb, view)
	return ctx.Render(http.StatusOK, "message-context", data)
}
