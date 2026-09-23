package alborzbase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/labstack/echo/v4"
)

// Alborz used to learn of new mail only when a page was loaded, and even
// then served a listing up to listingFreshFor old without asking. Apple
// Mail is quicker because it holds an IMAP connection in IDLE (RFC 2177)
// and the server tells it the moment something lands.
//
// So does this, now: one watcher per signed-in account, on a connection
// of its own. It does not fetch anything and it does not render; it
// drops what is cached for the folder, so the next request builds the
// page from the server. That keeps the whole of it to one rule - the
// cache is what goes stale, so the cache is what a notification touches.
//
// The connection is separate on purpose. DoIMAP serialises on the
// session's own connection, and an IDLE there would hold the lock for as
// long as it waited - which is to say until mail arrived - freezing
// every other page of that account.
const (
	// idleWindow bounds one IDLE. RFC 2177 asks a client to renew at
	// least every 29 minutes, because middleboxes drop a silent
	// connection long before the server does.
	idleWindow = 20 * time.Minute
	// idleRetry is the first wait after a failure. It doubles up to
	// idleMaxRetry, because a server that is refusing connections is
	// made worse by a client that keeps asking at a fixed rate - which
	// is how a watcher turns an outage into a login storm.
	idleRetry    = 30 * time.Second
	idleMaxRetry = 15 * time.Minute
	// sweepWindow is how often a server without NOTIFY is asked about
	// every other folder (ADR 45): short enough that mail a rule files
	// is seen while the reader still wonders, long enough that an
	// account with many folders is not asking all the time.
	sweepWindow = 2 * time.Minute
)

var watchers = &watcherSet{running: make(map[string]bool)}

type watcherSet struct {
	mu      sync.Mutex
	running map[string]bool
}

// watchAccount follows the account from the moment it is signed in, so
// mail that arrives while nobody is looking is noticed rather than
// waited for.
func watchAccount(ctx *alborz.Context, s *alborz.Session) {
	Watch(s, ctx.Logger(), ctx.Server.Changes)
}

// warmAccount fetches the account's INBOX views and rail as it is
// signed in, so the first click on mail finds them cached.
func warmAccount(ctx *alborz.Context, s *alborz.Session) {
	log := ctx.Logger()
	senderBookFor(s)
	SuggestAuthServ(ctx)
	go func() {
		if err := warmInbox(s); err != nil {
			log.Printf("warm %s: %v", s.Username(), err)
		}
	}()
}

// Watch starts following an account's INBOX if nothing is following it
// already. One watcher per account, however many browsers are signed in
// to it: they all read the same cache.
func Watch(s *alborz.Session, log echo.Logger, changes *alborz.Changes) {
	user := s.Username()

	watchers.mu.Lock()
	if watchers.running[user] {
		watchers.mu.Unlock()
		return
	}
	watchers.running[user] = true
	watchers.mu.Unlock()

	go func() {
		defer func() {
			watchers.mu.Lock()
			delete(watchers.running, user)
			watchers.mu.Unlock()
		}()
		watch(s, log, changes)
	}()
}

// watch keeps one connection in IDLE for as long as the session lives,
// reconnecting when the connection or the server gives out.
func watch(s *alborz.Session, log echo.Logger, changes *alborz.Changes) {
	backoff := idleRetry
	reconnect := false
	for {
		select {
		case <-s.Done():
			return
		default:
		}

		err := follow(s, changes, reconnect)
		reconnect = true
		if err != nil {
			log.Printf("watch %s: %v (retrying in %v)", s.Username(), err, backoff)
			select {
			case <-s.Done():
				return
			case <-time.After(backoff):
			}
			if backoff *= 2; backoff > idleMaxRetry {
				backoff = idleMaxRetry
			}
			continue
		}
		backoff = idleRetry
	}
}

// follow keeps one connection and re-enters IDLE on it. Reconnecting per
// notification is what the first version did, and it costs a TCP
// handshake, a TLS handshake and a LOGIN for every message that arrives
// - which a provider counting connections and logins reads as abuse, and
// answers by refusing the account.
func follow(s *alborz.Session, changes *alborz.Changes, reconnect bool) error {
	user := s.Username()
	changed := make(chan struct{}, 1)
	var overflowed atomic.Bool
	// The pages go stale here, on the connection's own reader, and not
	// where the change is picked up: a FETCH that follows an EXPUNGE in
	// one read names its message by the new numbering already.
	c, err := s.WatchIMAP(func() {
		mailboxStale(user, "INBOX")
		notify(changed)
	}, func(seqNum uint32, flags []imap.Flag) {
		mailboxFlagsAnnounced(user, "INBOX", seqNum, flags)
	}, func(mailbox string) {
		// INBOX speaks as the selected mailbox and is warmed before it
		// is announced; this is every other folder, under NOTIFY.
		if mailbox != "INBOX" {
			mailboxStale(user, mailbox)
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
		// Nothing to wait on. Sleeping the window keeps the loop from
		// spinning against a server that will never push.
		select {
		case <-s.Done():
		case <-time.After(idleWindow):
		}
		return nil
	}

	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return fmt.Errorf("failed to select INBOX: %w", err)
	}
	// Reconnection can have missed changes while no watcher was
	// listening, and since every folder is watched, in any folder.
	if reconnect {
		accountChanged(user)
		if err := warmInbox(s); err != nil {
			return err
		}
	}

	window, sweep := idleWindow, (func() error)(nil)
	if !armNotify(c) {
		window, sweep = sweepWindow, folderSweep(user, c, changes)
		// The first sweep is the baseline, taken now: taken at the first
		// wake instead, what a rule filed in the meantime would be the
		// baseline itself, and never announced.
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
		if err := idleOnce(s, c, changed, changes, window); err != nil {
			return err
		}
		// The server stopped saying: what it did not say is unknown, so
		// nothing cached is trusted, and reconnecting arms NOTIFY again.
		if overflowed.Load() {
			accountChanged(user)
			return errors.New("the server stopped notifying")
		}
		if sweep != nil {
			if err := sweep(); err != nil {
				return err
			}
		}
	}
}

// armNotify asks the server for news of every folder on this connection
// (RFC 5465). Offered and accepted, or the watcher sweeps instead: an
// older Dovecot and most other servers do not offer it, and a server
// that offers it can still refuse the set asked for.
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

// folderSweep watches every folder but INBOX on a server that will not
// say when one moves: each wake of the watcher asks each folder for its
// UIDNEXT and announces the ones that moved since the last time.
func folderSweep(user string, c *imapclient.Client, changes *alborz.Changes) func() error {
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
				// Gone between the LIST and here; the next sweep lists
				// what there is.
				continue
			}
			last, known := seen[f.Mailbox]
			seen[f.Mailbox] = status.UIDNext
			if known && status.UIDNext != last {
				mailboxStale(user, f.Mailbox)
				changes.Announce(user, f.Mailbox)
			}
		}
		return nil
	}
}

// On an IDLE change, keep serving the old listing until its replacement is ready.
func idleOnce(s *alborz.Session, c *imapclient.Client, changed chan struct{}, changes *alborz.Changes, length time.Duration) error {
	cmd, err := c.Idle()
	if err != nil {
		return fmt.Errorf("failed to idle: %w", err)
	}

	window := time.NewTimer(length)
	defer window.Stop()
	select {
	case <-changed:
		if err := warmInbox(s); err != nil {
			return fmt.Errorf("failed to refetch INBOX: %w", err)
		}
		// The page is told after the listing is back, so a browser
		// that acts on the news finds it ready.
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
func warmInbox(s *alborz.Session) error {
	settings, err := LoadSettings(s.Store())
	if err != nil {
		return err
	}
	user := s.Username()
	generation := listings.epoch(user)
	// The page size is the reader's, and a first page of another size
	// is a miss: warming the default over a reader who set a hundred
	// replaced their page with one they never get.
	perPage := listings.pageSize(user, "INBOX", defaultMessagesPerPage)
	own, err := listings.load(context.Background(), user, "INBOX", perPage, false, readOn(s, alborz.IMAPForeground, alborz.RoundTripTimeout, func(c *imapclient.Client) (*listingEntry, error) {
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
	listings.storeAt(user, listingView("#INBOX", "", "", "", ""), merged, generation)
	return nil
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
