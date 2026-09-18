package alborzbase

import (
	"context"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-imap/v2/imapclient"
)

func listingBudget(spec listingSpec) (alborz.IMAPClass, time.Duration) {
	if ParseQuery(spec.query).WantsText() {
		return alborz.IMAPScan, alborz.ScanTimeout
	}
	return alborz.IMAPForeground, alborz.RoundTripTimeout
}

// readOn is a listing's read as the cache's load takes it: one turn on
// the session's connection, under the budget of its class.
func readOn(s *alborz.Session, class alborz.IMAPClass, bound time.Duration, fetch func(*imapclient.Client) (*listingEntry, error)) func(context.Context) (*listingEntry, error) {
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

// cachedListing is a view as the cache holds it, a stale entry checked
// behind the page, and read through the cache when it holds none.
// Revalidation also covers gaps while the IDLE connection is down.
func cachedListing(ctx *alborz.Context, s *alborz.Session, view string, size int, spec listingSpec, folder func(*imapclient.Client) (string, error), fetch func(*imapclient.Client) (*listingEntry, error)) (*listingEntry, error) {
	if e, state := listings.lookup(s.Username(), view, size); e != nil {
		alborz.CacheTiming(ctx.Request().Context(), "listing", true)
		if state == listingStale {
			revalidate(s, view, e, folder, fetch)
		}
		return e, nil
	}
	return loadListing(ctx, s, view, size, spec, fetch)
}

func loadListing(ctx *alborz.Context, s *alborz.Session, view string, size int, spec listingSpec, fetch func(*imapclient.Client) (*listingEntry, error)) (*listingEntry, error) {
	alborz.CacheTiming(ctx.Request().Context(), "listing", false)
	class, bound := listingBudget(spec)
	return listings.load(ctx.Request().Context(), s.Username(), view, size, false, readOn(s, class, bound, fetch))
}

func prefetchPage(s *alborz.Session, settings *Settings, spec listingSpec, page, perPage int) {
	key := listingPage(listingView(spec.mbox, spec.query, spec.view, spec.sortKey, spec.sortDir), page, perPage)
	if e, _ := listings.lookup(s.Username(), key, perPage); e != nil {
		return
	}
	if !listings.claim(s.Username(), key) {
		return
	}
	go func() {
		defer listings.release(s.Username(), key)
		listings.load(context.Background(), s.Username(), key, perPage, true, readOn(s, alborz.IMAPBackground, alborz.RoundTripTimeout, func(c *imapclient.Client) (*listingEntry, error) {
			return fetchListing(c, s.Username(), spec, settings, page, perPage)
		}))
		// Bodies are fetched only for pages actually visited. Speculative
		// bodies would evict the page the reader is still using.
	}()
}
