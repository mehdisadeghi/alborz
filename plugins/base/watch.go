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
	c, err := s.WatchIMAP(func() { notify(changed) })
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

// idleOnce waits on one IDLE. A change is not read here: the listing is
// dropped and the next request fetches it, which keeps one place
// deciding what a listing looks like.
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
	case <-window.C:
	case <-s.Done():
	}

	if err := cmd.Close(); err != nil {
		return fmt.Errorf("failed to leave idle: %w", err)
	}
	return nil
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
