package mail

import (
	"context"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail/search"
	"github.com/emersion/go-imap/v2/imapclient"
)

func listingBudget(spec listingSpec) (alborz.IMAPClass, time.Duration) {
	if search.Parse(spec.query).WantsText() {
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

func (p *plugin) cachedListing(ctx *alborz.Context, s *alborz.Login, view string, size int, spec listingSpec, folder func(*imapclient.Client) (string, error), fetch func(*imapclient.Client) (*listingEntry, error)) (*listingEntry, error) {
	// A folder the watcher saw change is read again: its count beside the folder already moved.
	if e, state := p.listings.lookup(s.Username(), view, size); e != nil && state != listingChanged {
		alborz.CacheTiming(ctx.Request().Context(), "listing", true)
		if state == listingStale {
			p.revalidate(s, view, e, folder, fetch)
		}
		return e, nil
	}
	return p.loadListing(ctx, s, view, size, spec, fetch)
}

func (p *plugin) loadListing(ctx *alborz.Context, s *alborz.Login, view string, size int, spec listingSpec, fetch func(*imapclient.Client) (*listingEntry, error)) (*listingEntry, error) {
	alborz.CacheTiming(ctx.Request().Context(), "listing", false)
	class, bound := listingBudget(spec)
	return p.listings.load(ctx.Request().Context(), s.Username(), view, size, false, readOn(s, class, bound, fetch))
}

func (p *plugin) prefetchPage(s *alborz.Login, settings *Settings, spec listingSpec, page, perPage int) {
	key := listingPage(listingView(spec.mbox, spec.query, spec.view, spec.sortKey, spec.sortDir), page, perPage)
	if e, _ := p.listings.lookup(s.Username(), key, perPage); e != nil {
		return
	}
	if !p.listings.claim(s.Username(), key) {
		return
	}
	go func() {
		defer p.listings.release(s.Username(), key)
		p.listings.load(context.Background(), s.Username(), key, perPage, true, readOn(s, alborz.IMAPBackground, alborz.RoundTripTimeout, func(c *imapclient.Client) (*listingEntry, error) {
			return fetchListing(c, s.Username(), spec, settings, page, perPage)
		}))
		// Only visited pages get bodies; speculative ones would evict the page in use.
	}()
}
