package mail

import (
	"context"
	"log"
	"slices"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/search"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Place is where a message lies in its list, and the messages either side.
type Place struct {
	Newer, Older    imap.UID
	Position, Total int
}

// messageList is the order a message's list is cut from, and whether that is the folder's own.
func messageList(mailbox string, l ListAsk) (order string, own bool) {
	if l.Sort == SortThread {
		return orderView(mailbox, l.Query, l.View, SortThread, true), false
	}
	sortKey, reverse := l.order()
	return orderView(mailbox, l.Query, l.View, sortKey, reverse), l.Query == "" && l.View == "" && sortKey == "" && reverse
}

// A narrowed, sorted or threaded list places the message in its held order; the folder's own, in its held pages.
func (p *Mailboxes) cachedPlace(user, mailbox string, uid imap.UID, l ListAsk) (cached *listingEntry, place Place, placed bool) {
	sortKey, _ := l.order()
	cached, place, placed = p.listings.message(user, listingView(mailbox, l.Query, l.View, sortKey, l.Dir), uid, l.Page.Size)
	if order, own := messageList(mailbox, l); !own {
		if o, ok := p.listings.heldOrder(user, order); ok {
			return cached, placeIn(o.uids, uid), true
		}
	}
	return cached, place, placed
}

func (p *Mailboxes) placeInList(c *imapclient.Client, user string, settings *Settings, mailbox string, uid imap.UID, l ListAsk) (Place, error) {
	if err := ensureMailboxSelected(c, mailbox); err != nil {
		return Place{}, err
	}
	order, own := messageList(mailbox, l)
	algorithm := ThreadAlgorithm(c)
	threaded := l.Sort == SortThread
	if own || threaded && algorithm == "" {
		return folderNeighbors(c, uid)
	}
	o, err := p.listings.order(user, order, func() (messageOrder, error) {
		criteria := listCriteria(c, settings, l.Query, l.View)
		if threaded {
			return threadOrder(c, algorithm, criteria)
		}
		sortKey, reverse := l.order()
		uids, err := viewOrder(c, search.Parse(l.Query), criteria, sortKey, reverse)
		return messageOrder{uids: uids}, err
	})
	if err != nil {
		return Place{}, err
	}
	return placeIn(o.uids, uid), nil
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

// Around is what a message's navigation shows: the message, its place in its list and its thread.
type Around struct {
	Message         *IMAPMessage
	Place           Place
	ThreadSupported bool
	InReplyTo       *ThreadNeighbour
	Answers         []ThreadNeighbour
}

// Around reads a message's navigation, from the listing held where it can; merged says its list is read
// elsewhere, and sb gets the folder's counts where it lacks them.
func (p *Mailboxes) Around(ctx context.Context, login *alborz.Login, settings *Settings, sb *Rail, mailbox string, uid imap.UID, list ListAsk, merged bool) (Around, error) {
	var a Around
	cached, place, placed := p.cachedPlace(login.Username(), mailbox, uid, list)
	if merged {
		place, placed = Place{}, true
	}
	if cached != nil {
		a.Message = cached.row(uid)
		a.ThreadSupported = cached.threadAlgorithm != ""
	}
	err := login.DoIMAPContext(ctx, alborz.RoundTripTimeout, func(c *imapclient.Client) error {
		if err := ensureMailboxSelected(c, mailbox); err != nil {
			return err
		}
		if a.Message == nil {
			header := &imap.FetchItemBodySection{Peek: true, Specifier: imap.PartSpecifierHeader,
				HeaderFields: []string{"References", "List-Id"}}
			buf, err := fetchOne(c, uid, &imap.FetchOptions{UID: true, Envelope: true,
				BodySection: []*imap.FetchItemBodySection{header}})
			if err != nil {
				return err
			}
			msg := &IMAPMessage{FetchMessageBuffer: buf, Mailbox: mailbox}
			if h, err := readHeaderBytes(buf.FindBodySection(header)); err == nil {
				msg.setListHeaders(h)
			}
			a.Message = msg
		}
		if !placed {
			var err error
			if place, err = p.placeInList(c, login.Username(), settings, mailbox, uid, list); err != nil {
				return err
			}
		}
		a.ThreadSupported = ThreadAlgorithm(c) != ""
		a.InReplyTo, a.Answers = readThread(p.server.Logger(), c, mailbox, sentFolder(sb.Mailboxes), a.Message)
		if sb.Active == nil {
			st, err := c.Status(mailbox, listingStatusOptions(c)).Wait()
			if err != nil {
				return err
			}
			sb.Active = &MailboxStatus{StatusData: st}
		}
		return nil
	})
	a.Place = place
	return a, err
}

// Origin is a list spanning accounts or folders that a message was opened from: a merged folder, or a search.
type Origin struct {
	Role   string
	Search bool
	Logins []*alborz.Login
	Ask    ListAsk
}

// Beside places a message among the rows of the list it was opened from, reading once a page further
// where it is the last row read. position counts from one, and is zero where the list lacks the
// message; down names the accounts whose servers did not answer.
func (p *Mailboxes) Beside(ctx context.Context, settings *Settings, o Origin, account, mailbox string, uid imap.UID) (prev, next *IMAPMessage, position, total int, down []string, err error) {
	ask := o.Ask
	for read := range 2 {
		var e *listingEntry
		var more []string
		if o.Search {
			e, more, err = p.searchWindow(ctx, o.Logins, ask, settings)
		} else {
			e, _, more, err = p.mergedWindow(ctx, o.Logins, o.Role, ask, settings, true)
		}
		if err != nil {
			return nil, nil, 0, 0, down, err
		}
		down = append(down, more...)
		i := slices.IndexFunc(e.msgs, func(r IMAPMessage) bool {
			return r.Account == account && r.Mailbox == mailbox && r.UID == uid
		})
		if i < 0 {
			return nil, nil, 0, 0, down, nil
		}
		prev, next, position, total = nil, nil, i+1, e.total
		if i > 0 {
			prev = &e.msgs[i-1]
		}
		if i+1 < len(e.msgs) {
			next = &e.msgs[i+1]
		}
		if next != nil || len(e.msgs) >= e.total || read > 0 {
			break
		}
		ask.Page.Number = len(e.msgs) / ask.Page.Size
	}
	return prev, next, position, total, down, nil
}
