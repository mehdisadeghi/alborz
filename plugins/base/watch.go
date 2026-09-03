package alborzbase

import (
	"fmt"
	"sync"
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
	Watch(s, ctx.Logger())
}

// warmAccount fetches the account's INBOX views and rail as it is
// signed in, so the first click on mail finds them cached.
func warmAccount(ctx *alborz.Context, s *alborz.Session) {
	log := ctx.Logger()
	go func() {
		if err := warmInbox(s); err != nil {
			log.Printf("warm %s: %v", s.Username(), err)
		}
	}()
}

// Watch starts following an account's INBOX if nothing is following it
// already. One watcher per account, however many browsers are signed in
// to it: they all read the same cache.
func Watch(s *alborz.Session, log echo.Logger) {
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
		watch(s, log)
	}()
}

// watch keeps one connection in IDLE for as long as the session lives,
// reconnecting when the connection or the server gives out.
func watch(s *alborz.Session, log echo.Logger) {
	backoff := idleRetry
	for {
		select {
		case <-s.Done():
			return
		default:
		}

		if err := follow(s); err != nil {
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
func follow(s *alborz.Session) error {
	changed := make(chan struct{}, 1)
	c, err := s.WatchIMAP(func() { notify(changed) }, func(seqNum uint32, flags []imap.Flag) {
		listings.setFlags(s.Username(), "INBOX", seqNum, flags)
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

	for {
		select {
		case <-s.Done():
			return nil
		default:
		}
		if err := idleOnce(s, c, changed); err != nil {
			return err
		}
	}
}

// idleOnce waits on one IDLE. On a change the listing is dropped and
// fetched again at once, on the session's own connection, so the visit
// that follows new mail finds the page ready.
func idleOnce(s *alborz.Session, c *imapclient.Client, changed chan struct{}) error {
	cmd, err := c.Idle()
	if err != nil {
		return fmt.Errorf("failed to idle: %w", err)
	}

	window := time.NewTimer(idleWindow)
	defer window.Stop()
	select {
	case <-changed:
		listings.evict(s.Username(), "INBOX")
		if err := warmInbox(s); err != nil {
			return fmt.Errorf("failed to refetch INBOX: %w", err)
		}
	case <-window.C:
	case <-s.Done():
	}

	if err := cmd.Close(); err != nil {
		return fmt.Errorf("failed to leave idle: %w", err)
	}
	return nil
}

// warmInbox fetches back what the eviction dropped: the account's INBOX
// listing, its share of the merged one, and the rail's counts. All
// three went with the folder, and any of them missing makes the next
// click pay on the reader's time.
func warmInbox(s *alborz.Session) error {
	settings, err := LoadSettings(s.Store())
	if err != nil {
		return err
	}
	user := s.Username()
	var own, merged *listingEntry
	err = s.DoIMAP(func(c *imapclient.Client) error {
		var err error
		if own, err = fetchListing(c, listingSpec{mbox: "INBOX"}, settings, 0, settings.MessagesPerPage); err != nil {
			return err
		}
		merged, err = fetchUnifiedAccount(c, user, "INBOX", listingSpec{}, settings, settings.MessagesPerPage, true)
		return err
	})
	if err != nil {
		return err
	}
	listings.store(user, "INBOX", own)
	if merged.snap != nil {
		listings.store(user, listingView("#INBOX", "", false, "", ""), merged)
	}
	if _, err := sidebarFor(s); err != nil {
		return err
	}
	return prefetchBodies(s, settings.PreferHTML, "INBOX", own.msgs)
}

// watching reports whether the account's INBOX is under IDLE, in which
// case its listing is current until the watcher says otherwise.
func (w *watcherSet) watching(user string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running[user]
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
