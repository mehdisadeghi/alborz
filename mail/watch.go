package mail

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync/atomic"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
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

func (p *plugin) watchAccount(ctx *web.Context, s *alborz.Login) {
	p.Watch(s, ctx.Logger(), ctx.Server.Changes)
}

func (p *plugin) warmAccount(_ *web.Context, s *alborz.Login) {
	p.Warm(s)
}

// Warm reads ahead what an account's first pages need: its senders, its authserv-id and its inbox.
func (p *plugin) Warm(s *alborz.Login) {
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
func (p *plugin) Watch(s *alborz.Login, log *log.Logger, changes *alborz.Changes) {
	user := s.Username()
	if _, running := p.watchers.LoadOrStore(user, true); running {
		return
	}
	go func() {
		defer p.watchers.Delete(user)
		p.watch(s, log, changes)
	}()
}

func (p *plugin) watch(s *alborz.Login, log *log.Logger, changes *alborz.Changes) {
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

func (p *plugin) follow(s *alborz.Login, changes *alborz.Changes, reconnect bool) error {
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
	if idle {
		if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("failed to select INBOX: %w", err)
		}
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
		if err := p.idleOnce(s, c, changed, changes, window); err != nil {
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

func (p *plugin) folderSweep(user string, c *imapclient.Client, changes *alborz.Changes) func() error {
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
			status, err := c.Status(f.Mailbox, &imap.StatusOptions{UIDNext: true}).Wait()
			if err != nil {
				continue
			}
			last, known := seen[f.Mailbox]
			seen[f.Mailbox] = status.UIDNext
			if known && status.UIDNext != last {
				p.mailboxStale(user, f.Mailbox)
				changes.Announce(user, f.Mailbox)
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
func (p *plugin) idleOnce(s *alborz.Login, c *imapclient.Client, changed chan struct{}, changes *alborz.Changes, length time.Duration) error {
	cmd, err := c.Idle()
	if err != nil {
		return fmt.Errorf("failed to idle: %w", err)
	}

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
	return nil
}

func (p *plugin) warmInbox(s *alborz.Login) error {
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
	merged.sb = sidebar{}
	for i := range merged.msgs {
		merged.msgs[i].Account = user
	}
	p.listings.storeAt(user, listingView("#INBOX", "", "", "", ""), merged, generation)
	return nil
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
