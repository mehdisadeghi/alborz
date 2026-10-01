package mail

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// The watcher's connection is its own: an IDLE on the login's would hold DoIMAP's lock until mail came.
const (
	// Renews IDLE inside RFC 2177's 29 minutes.
	idleWindow = 20 * time.Minute
	// Doubles up to idleMaxRetry, so an outage does not become a login storm.
	idleRetry    = 30 * time.Second
	idleMaxRetry = 15 * time.Minute
	// sweepWindow paces the folder sweep on a server without NOTIFY (ADR 45).
	sweepWindow = 2 * time.Minute
)

// Warm reads ahead what an account's first pages need: its senders, its authserv-id and its inbox.
func (p *Mailboxes) Warm(s *alborz.Login) {
	p.senderBookFor(s)
	p.SuggestAuthServ(s)
	if !p.listings.keeps() {
		return
	}
	log := p.server.Logger()
	go func() {
		if err := p.warmInbox(s); err != nil {
			log.Printf("warm %s: %v", s.Username(), err)
		}
	}()
}

// Watch starts following an account's folders unless a watcher already is.
func (p *Mailboxes) Watch(s *alborz.Login, log *log.Logger, changes *alborz.Changes) {
	user := s.Username()
	if _, running := p.watchers.LoadOrStore(user, true); running {
		return
	}
	go func() {
		p.watch(s, log, changes)
		p.watchers.Delete(user)
		// Another browser's login of the account, one kept for notifications among them, follows on.
		for _, next := range p.server.Logins() {
			if next.Username() == user {
				p.Watch(next, log, changes)
				return
			}
		}
	}()
}

func (p *Mailboxes) watch(s *alborz.Login, log *log.Logger, changes *alborz.Changes) {
	backoff := idleRetry
	reconnect := false
	for {
		select {
		case <-s.Done():
			return
		default:
		}

		err := p.follow(s, changes, reconnect)
		reconnect = true
		if err != nil {
			log.Printf("watch %s: %v (retrying in %v)", s.Username(), err, backoff)
			select {
			case <-s.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, idleMaxRetry)
			continue
		}
		backoff = idleRetry
	}
}

func (p *Mailboxes) follow(s *alborz.Login, changes *alborz.Changes, reconnect bool) error {
	user := s.Username()
	changed := make(chan struct{}, 1)
	var overflowed atomic.Bool
	// Handled on the connection's reader, since sequence numbers shift with each EXPUNGE.
	c, err := s.WatchIMAP(func() {
		p.mailboxStale(user, "INBOX")
		notify(changed)
	}, func(seqNum uint32, flags []imap.Flag) {
		p.mailboxFlagsAnnounced(user, "INBOX", seqNum, flags)
	}, func(mailbox string) {
		// INBOX is reported as the selected mailbox.
		if mailbox != "INBOX" {
			p.mailboxStale(user, mailbox)
			changes.Announce(user, mailbox)
		}
	}, func() {
		overflowed.Store(true)
		notify(changed)
	})
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer c.Close()

	idle := c.Caps().Has(imap.CapIdle)
	var validity uint32
	if idle {
		selected, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return fmt.Errorf("failed to select INBOX: %w", err)
		}
		validity = selected.UIDValidity
	}
	// A reconnection may have missed changes in any folder.
	if reconnect {
		p.accountChanged(user)
		if err := p.warmInbox(s); err != nil {
			return err
		}
	}
	if !idle {
		return sweepEvery(s.Done(), p.folderSweep(user, c, changes), idleWindow)
	}

	window, sweep := idleWindow, (func() error)(nil)
	if !armNotify(c) {
		window, sweep = sweepWindow, p.folderSweep(user, c, changes)
		// Baseline now, or what a rule files before the first wake is never announced.
		if err := sweep(); err != nil {
			return err
		}
	}
	for {
		select {
		case <-s.Done():
			return nil
		default:
		}
		if err := p.idleOnce(s, c, validity, changed, changes, window); err != nil {
			return err
		}
		// What the server stopped saying is unknown; reconnecting arms NOTIFY again.
		if overflowed.Load() {
			p.accountChanged(user)
			return errors.New("the server stopped notifying")
		}
		if sweep != nil {
			if err := sweep(); err != nil {
				return err
			}
		}
	}
}

func armNotify(c *imapclient.Client) bool {
	if !c.Caps().Has(imap.CapNotify) {
		return false
	}
	both := []imap.NotifyEvent{imap.NotifyEventMessageNew, imap.NotifyEventMessageExpunge}
	cmd, err := c.Notify(&imap.NotifyOptions{Items: []imap.NotifyItem{
		{MailboxSpec: imap.NotifyMailboxSpecSelected, Events: append(both, imap.NotifyEventFlagChange)},
		{MailboxSpec: imap.NotifyMailboxSpecPersonal, Events: both},
	}})
	return err == nil && cmd.Wait() == nil
}

func (p *Mailboxes) folderSweep(user string, c *imapclient.Client, changes *alborz.Changes) func() error {
	seen := map[string]imap.UID{}
	// The selected folder is IDLE's to report; without IDLE none is selected.
	var selected string
	if m := c.Mailbox(); m != nil {
		selected = m.Name
	}
	return func() error {
		folders, err := c.List("", "*", nil).Collect()
		if err != nil {
			return fmt.Errorf("failed to list folders: %w", err)
		}
		for _, f := range folders {
			if f.Mailbox == selected || slices.Contains(f.Attrs, imap.MailboxAttrNoSelect) ||
				slices.Contains(f.Attrs, imap.MailboxAttrNonExistent) {
				continue
			}
			status, err := c.Status(f.Mailbox, &imap.StatusOptions{UIDNext: true, UIDValidity: true}).Wait()
			if err != nil {
				continue
			}
			last, known := seen[f.Mailbox]
			seen[f.Mailbox] = status.UIDNext
			if known && status.UIDNext != last {
				p.mailboxStale(user, f.Mailbox)
				changes.Announce(user, f.Mailbox)
				// UIDNEXT grows only with a message added (RFC 9051 section 2.3.1.1).
				if strings.EqualFold(f.Mailbox, "INBOX") && status.UIDNext > last {
					p.newMail(user, status.UIDValidity, status.UIDNext-1)
				}
			}
		}
		return nil
	}
}

// Without IDLE, new mail is noticed only by sweeping.
func sweepEvery(done <-chan struct{}, sweep func() error, window time.Duration) error {
	for {
		if err := sweep(); err != nil {
			return err
		}
		select {
		case <-done:
			return nil
		case <-time.After(window):
		}
	}
}

// On an IDLE change, keep serving the old listing until its replacement is ready.
func (p *Mailboxes) idleOnce(s *alborz.Login, c *imapclient.Client, validity uint32, changed chan struct{}, changes *alborz.Changes, length time.Duration) error {
	cmd, err := c.Idle()
	if err != nil {
		return fmt.Errorf("failed to idle: %w", err)
	}

	held := inboxCount(c)
	window := time.NewTimer(length)
	defer window.Stop()
	select {
	case <-changed:
		if err := p.warmInbox(s); err != nil {
			return fmt.Errorf("failed to refetch INBOX: %w", err)
		}
		// Told after the listing is back, so a browser acting on it finds it ready.
		changes.Announce(s.Username(), "INBOX")
	case <-window.C:
	case <-s.Done():
	}

	if err := cmd.Close(); err != nil {
		return fmt.Errorf("failed to leave idle: %w", err)
	}
	if count := inboxCount(c); count > held {
		// The newest message names what arrived; UIDs only grow (RFC 9051 section 2.3.1.1).
		msgs, err := c.Fetch(imap.SeqSetNum(count), &imap.FetchOptions{UID: true}).Collect()
		if err != nil {
			return fmt.Errorf("failed to read the newest message: %w", err)
		}
		if len(msgs) == 1 {
			p.newMail(s.Username(), validity, msgs[0].UID)
		}
	}
	return nil
}

func (p *Mailboxes) warmInbox(s *alborz.Login) error {
	if !p.listings.keeps() {
		return nil
	}
	settings, err := LoadSettings(context.Background(), s.Store())
	if err != nil {
		return err
	}
	user := s.Username()
	generation := p.listings.epoch(user)
	// Warmed at the reader's page size: another size is a miss.
	perPage := p.listings.pageSize(user, "INBOX", defaultMessagesPerPage)
	own, err := p.listings.load(context.Background(), user, "INBOX", perPage, false, readOn(s, alborz.IMAPForeground, alborz.RoundTripTimeout, func(c *imapclient.Client) (*listingEntry, error) {
		return p.fetchListing(c, user, listingSpec{mbox: "INBOX"}, settings, alborz.Page{Size: perPage})
	}))
	if err != nil {
		return err
	}
	merged := own.snapshot()
	merged.sb = Rail{}
	for i := range merged.msgs {
		merged.msgs[i].Account = user
	}
	p.listings.storeAt(user, listingView("#INBOX", "", "", "", ""), merged, generation)
	return nil
}

// inboxCount is the selected INBOX's EXISTS, which the client lowers with each EXPUNGE.
func inboxCount(c *imapclient.Client) uint32 {
	if m := c.Mailbox(); m != nil {
		return m.NumMessages
	}
	return 0
}

func (p *Mailboxes) newMail(account string, validity uint32, newest imap.UID) {
	if p.NewMail != nil {
		p.NewMail(account, validity, newest)
	}
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
