package mail

import (
	"context"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/imapsearch"
	"git.mehdix.org/alborz/search"
	"github.com/emersion/go-imap/v2/imapclient"
)

func listingBudget(spec listingSpec) (alborz.IMAPClass, time.Duration) {
	if imapsearch.WantsText(search.Parse(spec.query)) {
		return alborz.IMAPScan, alborz.ScanTimeout
	}
	return alborz.IMAPForeground, alborz.RoundTripTimeout
}

func readOn(s *alborz.Login, class alborz.IMAPClass, bound time.Duration, fetch func(*imapclient.Client) (*listingEntry, error)) func(context.Context) (*listingEntry, error) {
	return func(work context.Context) (*listingEntry, error) {
		var e *listingEntry
		err := s.DoIMAPWork(work, class, bound, func(c *imapclient.Client) error {
			var err error
			e, err = fetch(c)
			return err
		})
		return e, err
	}
}

func (p *Mailboxes) listing(ctx context.Context, s *alborz.Login, view string, size int, spec listingSpec, folder func(*imapclient.Client) (string, error), fetch func(*imapclient.Client) (*listingEntry, error), cached bool) (*listingEntry, error) {
	if cached {
		// A folder the watcher saw change is read again: its count beside the folder already moved.
		if e, state := p.listings.lookup(s.Username(), view, size); e != nil && state != listingChanged {
			alborz.CacheTiming(ctx, "listing", true)
			if state == listingStale {
				p.revalidate(s, view, e, folder, fetch)
			}
			return e, nil
		}
	}
	alborz.CacheTiming(ctx, "listing", false)
	class, bound := listingBudget(spec)
	return p.listings.load(ctx, s.Username(), view, size, false, readOn(s, class, bound, fetch))
}

func (p *Mailboxes) prefetchPage(s *alborz.Login, settings *Settings, spec listingSpec, page alborz.Page) {
	key := listingPage(listingView(spec.mbox, spec.query, spec.view, spec.sortKey, spec.sortDir), page.Number, page.Size)
	if e, _ := p.listings.lookup(s.Username(), key, page.Size); e != nil {
		return
	}
	if !p.listings.claim(s.Username(), key) {
		return
	}
	go func() {
		defer p.listings.release(s.Username(), key)
		_, _ = p.listings.load(context.Background(), s.Username(), key, page.Size, true, readOn(s, alborz.IMAPBackground, alborz.RoundTripTimeout, func(c *imapclient.Client) (*listingEntry, error) {
			return p.fetchListing(c, s.Username(), spec, settings, page)
		}))
		// Only visited pages get bodies; speculative ones would evict the page in use.
	}()
}
