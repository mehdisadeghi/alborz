package mail

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/textproto"
)

type listPlace struct {
	newer, older    imap.UID
	position, total int
}

// messageList is the order a message's list is cut from, and whether that is the folder's own.
func messageList(mailbox string, l folderList) (order string, own bool) {
	if l.sort == threadSort {
		return orderView(mailbox, l.query, l.view, threadSort, true), false
	}
	sortKey, reverse := l.order()
	return orderView(mailbox, l.query, l.view, sortKey, reverse), l.query == "" && l.view == "" && sortKey == "" && reverse
}

// A narrowed, sorted or threaded list places the message in its held order; the folder's own, in its held pages.
func (p *plugin) cachedPlace(user, mailbox string, uid imap.UID, l folderList, perPage int) (cached *listingEntry, place listPlace, placed bool) {
	sortKey, _ := l.order()
	cached, place, placed = p.listings.message(user, listingView(mailbox, l.query, l.view, sortKey, l.dir), uid, perPage)
	if order, own := messageList(mailbox, l); !own {
		if o, ok := p.listings.heldOrder(user, order); ok {
			return cached, placeIn(o.uids, uid), true
		}
	}
	return cached, place, placed
}

func (p *plugin) placeInList(c *imapclient.Client, user string, settings *Settings, mailbox string, uid imap.UID, l folderList) (listPlace, error) {
	if err := ensureMailboxSelected(c, mailbox); err != nil {
		return listPlace{}, err
	}
	order, own := messageList(mailbox, l)
	algorithm := ThreadAlgorithm(c)
	threaded := l.sort == threadSort
	if own || threaded && algorithm == "" {
		return folderNeighbors(c, uid)
	}
	o, err := p.listings.order(user, order, func() (messageOrder, error) {
		criteria := listCriteria(c, settings, l.query, l.view)
		if threaded {
			return threadOrder(c, algorithm, criteria)
		}
		sortKey, reverse := l.order()
		uids, err := viewOrder(c, search.Parse(l.query), criteria, sortKey, reverse)
		return messageOrder{uids: uids}, err
	})
	if err != nil {
		return listPlace{}, err
	}
	return placeIn(o.uids, uid), nil
}

// mergedList is the list a message was opened from when that list spans
// accounts or folders: a merged folder or a search.
type mergedList struct {
	role   string
	search bool
	logins []*alborz.Login
	ask    listAsk
}

// mergedOrigin reads the list a message was opened from; nil where it is one folder's.
func mergedOrigin(ctx *web.Context) *mergedList {
	from := ctx.From()
	if from == "" {
		return nil
	}
	u, err := url.Parse(from)
	if err != nil {
		return nil
	}
	q := u.Query()
	logins := ctx.Logins()
	if account := q.Get("account"); account != "" {
		login := ctx.LoginFor(account)
		if login == nil {
			return nil
		}
		logins = []*alborz.Login{login}
	}
	ask, err := readListAskAt(ctx, q, "account")
	if err != nil {
		return nil
	}
	route, values, _ := ctx.Server.Match(u)
	if route == "mail.search" {
		ask.spec.query = strings.TrimSpace(ask.spec.query)
		if !StarView(ask.spec.view) {
			ask.spec.view = ""
		}
		return &mergedList{search: true, logins: logins, ask: ask}
	}
	if route != "mail.mailbox" || len(logins) < 2 {
		return nil
	}
	return &mergedList{role: values["mbox"], logins: logins, ask: ask}
}

// mergedNeighbours places a message among the merged rows it was opened
// from, reading once a page further down where it is the last row read.
func (p *plugin) mergedNeighbours(ctx *web.Context, settings *Settings, m *mergedList, mailbox string, uid imap.UID) (web.Neighbours, error) {
	account := ctx.Login.Username()
	ask := m.ask
	var n web.Neighbours
	for read := range 2 {
		var e *listingEntry
		var down []string
		var err error
		if m.search {
			e, down, err = p.searchWindow(ctx.Request().Context(), m.logins, ask, settings)
		} else {
			e, _, down, err = p.mergedWindow(ctx.Request().Context(), ctx.Logins(), m.role, ask, settings, true)
		}
		if err != nil {
			return web.Neighbours{}, err
		}
		ctx.Unreachable(down)
		i := slices.IndexFunc(e.msgs, func(r IMAPMessage) bool {
			return r.Account == account && r.Mailbox == mailbox && r.UID == uid
		})
		if i < 0 {
			return web.Neighbours{}, nil
		}
		n = web.Neighbours{Position: i + 1, Total: e.total}
		if i > 0 {
			n.PrevURL = neighbourHref(ctx, e.msgs[i-1].Account, e.msgs[i-1].Mailbox, e.msgs[i-1].UID)
		}
		if i+1 < len(e.msgs) {
			n.NextURL = neighbourHref(ctx, e.msgs[i+1].Account, e.msgs[i+1].Mailbox, e.msgs[i+1].UID)
		}
		if n.NextURL != "" || len(e.msgs) >= e.total || read > 0 {
			break
		}
		ask.page.Number = len(e.msgs) / ask.page.Size
	}
	return n, nil
}

func readThread(log *log.Logger, c *imapclient.Client, mailbox, sent string, msg *IMAPMessage) (*ThreadNeighbour, []ThreadNeighbour) {
	parent, err := threadParent(c, mailbox, sent, msg)
	if err != nil {
		log.Printf("thread lookup failed: %v", err)
	}
	if sent == "" || mailbox != sent {
		return parent, nil
	}
	answers, err := threadAnswers(c, mailbox, msg)
	if err != nil {
		log.Printf("answer lookup failed: %v", err)
	}
	return parent, answers
}

func (p *plugin) handleMessageNavigation(ctx *web.Context) error {
	mailbox, uid, err := MessageRef(ctx)
	if err != nil {
		return err
	}
	view, err := readView(ctx.QueryParams())
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
	merged := mergedOrigin(ctx)
	list := folderList{data.Query, view, ctx.QueryParam("sort"), ctx.QueryParam("dir")}
	around, err := p.around(ctx.Request().Context(), ctx.Login, settings, &sb, mailbox, uid, list, web.PerPage(ctx), merged != nil)
	if err != nil {
		return err
	}
	data.Message, data.ThreadSupported, data.InReplyTo, data.Answers = around.msg, around.threadSupported, around.inReplyTo, around.answers
	place := around.place
	data.Neighbours = neighbours(ctx, mailbox, place)
	if merged != nil {
		if data.Neighbours, err = p.mergedNeighbours(ctx, settings, merged, mailbox, uid); err != nil {
			return err
		}
	}
	data.IMAPBaseRenderData = *assembleIMAPBase(ctx, web.NewBaseRenderData(ctx), mailbox, sb, view)
	return ctx.Render(http.StatusOK, "message-context", data)
}

// messageAround is what a message's navigation shows: the message, its place in its list and its thread.
type messageAround struct {
	msg             *IMAPMessage
	place           listPlace
	threadSupported bool
	inReplyTo       *ThreadNeighbour
	answers         []ThreadNeighbour
}

// around reads a message's navigation, from the listing held where it can; merged says its list is read
// elsewhere, and sb gets the folder's counts where it lacks them.
func (p *plugin) around(ctx context.Context, login *alborz.Login, settings *Settings, sb *sidebar, mailbox string, uid imap.UID, list folderList, perPage int, merged bool) (messageAround, error) {
	var a messageAround
	cached, place, placed := p.cachedPlace(login.Username(), mailbox, uid, list, perPage)
	if merged {
		place, placed = listPlace{}, true
	}
	if cached != nil {
		a.msg = cached.row(uid)
		a.threadSupported = cached.threadAlgorithm != ""
	}
	err := login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mailbox); err != nil {
			return err
		}
		if a.msg == nil {
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
			a.msg = msg
		}
		if !placed {
			var err error
			if place, err = p.placeInList(c, login.Username(), settings, mailbox, uid, list); err != nil {
				return err
			}
		}
		a.threadSupported = ThreadAlgorithm(c) != ""
		a.inReplyTo, a.answers = readThread(p.server.Logger(), c, mailbox, sentFolder(sb.mailboxes), a.msg)
		if sb.active == nil {
			st, err := c.Status(mailbox, listingStatusOptions(c)).Wait()
			if err != nil {
				return err
			}
			sb.active = &MailboxStatus{StatusData: st}
		}
		return nil
	})
	a.place = place
	return a, err
}
