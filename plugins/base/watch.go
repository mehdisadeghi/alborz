package alborzbase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/labstack/echo/v4"
)

// One watcher per signed-in account holds a connection in IDLE (RFC
// 2177) and marks stale what the server announces. The connection is its
// own: an IDLE on the login's would hold DoIMAP's lock until mail came.
const (
	// idleWindow renews IDLE inside RFC 2177's 29 minutes, before
	// middleboxes drop a silent connection.
	idleWindow = 20 * time.Minute
	// idleRetry doubles up to idleMaxRetry, so an outage does not become
	// a login storm.
	idleRetry    = 30 * time.Second
	idleMaxRetry = 15 * time.Minute
	// sweepWindow is how often a server without NOTIFY is asked about
	// every other folder (ADR 45).
	sweepWindow = 2 * time.Minute
)

// watchAccount follows the account from the moment it is signed in.
func (p *plugin) watchAccount(ctx *alborz.Context, s *alborz.Login) {
	p.Watch(s, ctx.Logger(), ctx.Server.Changes)
}

// warmAccount fetches the account's INBOX views and rail as it is
// signed in, so the first click on mail finds them cached.
func (p *plugin) warmAccount(ctx *alborz.Context, s *alborz.Login) {
	p.senderBookFor(s)
	p.SuggestAuthServ(ctx)
	if !p.listings.keeps() {
		return
	}
	log := ctx.Logger()
	go func() {
		if err := p.warmInbox(s); err != nil {
			log.Printf("warm %s: %v", s.Username(), err)
		}
	}()
}

// Watch starts following an account's folders unless something already
// is: however many browsers are signed in, they read one cache.
func (p *plugin) Watch(s *alborz.Login, log echo.Logger, changes *alborz.Changes) {
	user := s.Username()
	if _, running := p.watchers.LoadOrStore(user, true); running {
		return
	}
	go func() {
		defer p.watchers.Delete(user)
		p.watch(s, log, changes)
	}()
}

// watch keeps one connection in IDLE for as long as the login lives,
// reconnecting when the connection or the server gives out.
func (p *plugin) watch(s *alborz.Login, log echo.Logger, changes *alborz.Changes) {
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

// follow keeps one connection and re-enters IDLE on it: a login per
// arrival is what a provider reads as abuse.
func (p *plugin) follow(s *alborz.Login, changes *alborz.Changes, reconnect bool) error {
	user := s.Username()
	changed := make(chan struct{}, 1)
	var overflowed atomic.Bool
	// The pages go stale on the connection's own reader: a FETCH after an
	// EXPUNGE in one read already names its message by the new numbers.
	c, err := s.WatchIMAP(func() {
		p.mailboxStale(user, "INBOX")
		notify(changed)
	}, func(seqNum uint32, flags []imap.Flag) {
		p.mailboxFlagsAnnounced(user, "INBOX", seqNum, flags)
	}, func(mailbox string) {
		// INBOX speaks as the selected mailbox; this is every other
		// folder, under NOTIFY.
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

	if !c.Caps().Has(imap.CapIdle) {
		// Nothing to wait on; sleeping keeps the loop from spinning.
		select {
		case <-s.Done():
		case <-time.After(idleWindow):
		}
		return nil
	}

	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return fmt.Errorf("failed to select INBOX: %w", err)
	}
	// A reconnection may have missed changes in any folder.
	if reconnect {
		p.accountChanged(user)
		if err := p.warmInbox(s); err != nil {
			return err
		}
	}

	window, sweep := idleWindow, (func() error)(nil)
	if !armNotify(c) {
		window, sweep = sweepWindow, p.folderSweep(user, c, changes)
		// The baseline is taken now, or what a rule files before the
		// first wake would never be announced.
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
		// What the server stopped saying is unknown; reconnecting arms
		// NOTIFY again.
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

// armNotify asks the server for news of every folder (RFC 5465); false
// when it does not offer or refuses it, and the watcher sweeps instead.
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

// folderSweep asks every folder but INBOX for its UIDNEXT and announces
// the ones that moved since the last sweep.
func (p *plugin) folderSweep(user string, c *imapclient.Client, changes *alborz.Changes) func() error {
	seen := map[string]imap.UID{}
	return func() error {
		folders, err := c.List("", "*", nil).Collect()
		if err != nil {
			return fmt.Errorf("failed to list folders: %w", err)
		}
		for _, f := range folders {
			if f.Mailbox == "INBOX" || slices.Contains(f.Attrs, imap.MailboxAttrNoSelect) ||
				slices.Contains(f.Attrs, imap.MailboxAttrNonExistent) {
				continue
			}
			status, err := c.Status(f.Mailbox, &imap.StatusOptions{UIDNext: true}).Wait()
			if err != nil {
				continue // gone since the LIST
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
		// Told after the listing is back, so a browser acting on it finds
		// it ready.
		changes.Announce(s.Username(), "INBOX")
	case <-window.C:
	case <-s.Done():
	}

	if err := cmd.Close(); err != nil {
		return fmt.Errorf("failed to leave idle: %w", err)
	}
	return nil
}

// warmInbox shares the first page fetch with login and foreground readers.
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
		return fetchListing(c, user, listingSpec{mbox: "INBOX"}, settings, 0, perPage)
	}))
	if err != nil {
		return err
	}
	// The default merged window is the same INBOX rows, qualified by account.
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
