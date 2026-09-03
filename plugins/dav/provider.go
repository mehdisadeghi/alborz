package dav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/davcache"
	"github.com/labstack/echo/v4"
)

// Kind tells a CalDAV provider from a CardDAV one: the URL schemes it
// is configured under and how its server is found from a bare domain.
type Kind struct {
	// Name is the plugin's, for the log; Label is what a reader calls
	// the service.
	Name, Label string
	// Schemes are the explicit upstream schemes, secure and plain.
	Schemes [2]string
	// Discover finds the service's URL for a domain by its SRV record.
	Discover func(ctx context.Context, domain string) (string, error)
	// FindHome is the home set behind the account's server. Principal
	// and home set are two round trips, asked through the kind's own
	// go-webdav client: the two share no interface to ask through.
	FindHome func(ctx context.Context, client *http.Client, endpoint string) (string, error)
	// List reads the kind's collections out of the home set.
	List func(ctx context.Context, client *http.Client, base *url.URL, home string) ([]Collection, error)
	// Make makes one at target, MKCALENDAR or extended MKCOL; an address
	// book is given no components.
	Make func(ctx context.Context, client *http.Client, target, name, color string, components []string) error
	// Unnamed is the address of a new collection whose name gives none.
	Unnamed string
}

// Age at which a discovered collection list is reloaded in the
// background while still being served; one made or removed by another
// client shows up one visit late at worst.
const discoveryTTL = 5 * time.Minute

// The discovery load is detached from its requester's context, so a
// deadline of its own is all that keeps a hung server from wedging
// every waiter behind the memo.
const discoveryTimeout = 30 * time.Second

// Provider is one DAV service across the served domains: where each
// domain's server is, the cache in front of it, and the client that
// talks to it on a session's behalf.
type Provider struct {
	kind  Kind
	urls  map[string]*url.URL // endpoint per served mail domain
	cache *davcache.Cache
	// found per username; see Collections.
	collections *alborz.Memo[[]Collection]

	// Set in debug mode; logs upstream DAV traffic.
	debug echo.Logger
}

// NewProvider resolves the service for every served domain. Nil without
// an error means no domain has it, and the plugin has nothing to serve.
// The URL of each domain's server is found by DNS alone, so startup
// never waits on a DAV host; asking whether it answers is a probe run
// in the background, and a request surfaces an unreachable one until
// it does.
func NewProvider(srv *alborz.Server, kind Kind) (*Provider, error) {
	urls := make(map[string]*url.URL)
	for _, domain := range srv.Domains() {
		u, err := domainURL(srv, kind, domain)
		if err != nil {
			return nil, err
		}
		if u != nil {
			urls[domain] = u
		}
	}
	if len(urls) == 0 {
		return nil, nil
	}
	p := &Provider{kind: kind, urls: urls, cache: davcache.New(),
		collections: alborz.NewBackgroundMemo[[]Collection](discoveryTTL)}
	if srv.Options.Debug {
		p.debug = srv.Logger()
	}
	p.cache.Start()
	// Signing out is the only thing that ends the cache's authority to
	// hold this account's collections; idleness no longer does.
	srv.OnAccountGone = append(srv.OnAccountGone, p.cache.Forget)

	// Asking whether a server answers may take seconds each; it runs
	// after the port is open, and a request surfaces an unreachable one
	// until it recovers.
	for domain, u := range p.urls {
		go func() {
			if err := SanityCheckURL(u); err != nil {
				srv.Logger().Printf("Warning: %s: domain %q: %s server %q not reachable at startup: %v", kind.Name, domain, kind.Label, u, err)
			}
		}()
	}
	return p, nil
}

// home is where the account's server lists its collections.
func (p *Provider) home(ctx context.Context, session *alborz.Session) (string, error) {
	u, _ := p.URL(session)
	return p.kind.FindHome(ctx, p.HTTPClient(session), u.String())
}

// Collections is the account's list of the kind, empty when it has
// none. Principal, home set, and list are three sequential round trips,
// so they are found once per user rather than on every page. The load
// outlives the request that starts it: a second page waiting on it must
// not be failed by the first one's reader going away.
func (p *Provider) Collections(ctx context.Context, session *alborz.Session) ([]Collection, error) {
	base, _ := p.URL(session)
	return p.collections.Get(session.Username(), func() ([]Collection, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryTimeout)
		defer cancel()

		home, err := p.home(ctx, session)
		if err != nil {
			return nil, err
		}
		infos, err := p.kind.List(ctx, p.HTTPClient(session), base, home)
		if err != nil {
			return nil, fmt.Errorf("failed to list the %s collections: %v", p.kind.Label, err)
		}
		return infos, nil
	})
}

// Forget drops the account's found list, for a change the next page
// has to show.
func (p *Provider) Forget(username string) { p.collections.Forget(username) }

// Opened is the session's client of the kind with the account's
// collections: what a page about one account starts from.
func Opened[C any](ctx context.Context, p *Provider, session *alborz.Session, client func(*alborz.Session) (C, error)) (C, []Collection, error) {
	c, err := client(session)
	if err != nil {
		return c, nil, err
	}
	infos, err := p.Collections(ctx, session)
	return c, infos, err
}

// Create adds a collection to the account's home, walking to the first
// free address, and forgets the found list, so the new one appears at
// once.
func (p *Provider) Create(ctx context.Context, session *alborz.Session, name, color string, components []string) error {
	base, _ := p.URL(session)
	home, err := p.home(ctx, session)
	if err != nil {
		return err
	}
	client := p.HTTPClient(session)
	err = CreateCollection(ctx, base, home, name, p.kind.Unnamed, func(ctx context.Context, target string) error {
		return p.kind.Make(ctx, client, target, name, color, components)
	})
	if err != nil {
		return err
	}
	p.Forget(session.Username())
	return nil
}

// URL resolves the session's endpoint, falling back to the unnamed
// provider's.
func (p *Provider) URL(session *alborz.Session) (*url.URL, bool) {
	u, ok := p.urls[session.Domain()]
	if !ok {
		u, ok = p.urls[""]
	}
	return u, ok
}

// HTTPClient talks to the session's server through the cache, with the
// account's credentials on every request.
func (p *Provider) HTTPClient(session *alborz.Session) *http.Client {
	return httpClient(p.cache, session, p.debug)
}

// CountObjects counts a collection's objects for the session, lazily:
// -1 means the server did not answer, which the page states rather
// than guessing at.
func (p *Provider) CountObjects(ctx *alborz.Context, coll string) func() int {
	return func() int {
		base, _ := p.URL(ctx.Session)
		n, err := CountObjects(ctx.Request().Context(), p.HTTPClient(ctx.Session), base, coll)
		if err != nil {
			ctx.Logger().Printf("%s: failed to count %s: %v", p.kind.Name, coll, err)
			return -1
		}
		return n
	}
}

// Enabled reports whether any signed-in account has the service: the
// pooled pages exist when one does.
func (p *Provider) Enabled(ctx *alborz.Context) bool {
	if ctx.Session == nil {
		return false
	}
	for _, s := range ctx.Sessions() {
		if _, ok := p.URL(s); ok {
			return true
		}
	}
	return false
}

// Guarded is a section's route for an account that may have none of
// the kind, which is a state to explain and not an error: the account
// is told in words that it has no such service.
func (p *Provider) Guarded(none error, words string, h func(*alborz.Context) error) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		err := h(ctx)
		if errors.Is(err, none) {
			return alborz.RenderInfo(ctx, http.StatusOK, fmt.Sprintf(ctx.T(words), ctx.Session.Username()))
		}
		return err
	}
}

// HandleChoose keeps which collections of list the reader ticked, in
// the kind's own settings, and lands on the list.
func HandleChoose(list, field string, keep func(store alborz.Store, paths []string) error) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		if err := keep(ctx.Session.Store(), params[field]); err != nil {
			return fmt.Errorf("failed to save the chosen collections: %w", err)
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr(list))
	}
}

// Close stops the cache's refresh loop.
func (p *Provider) Close() error {
	p.cache.Stop()
	return nil
}

// domainURL resolves the domain's endpoint; nil without error means the
// domain has none. It reads DNS and config only, so startup never waits
// on the server itself.
func domainURL(srv *alborz.Server, kind Kind, domain string) (*url.URL, error) {
	secure, plain := kind.Schemes[0], kind.Schemes[1]
	u, err := srv.Upstream(domain, secure, plain, "https", "http+insecure")
	if _, ok := err.(*alborz.NoUpstreamError); ok {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("%s: domain %q: failed to parse upstream %s server: %v", kind.Name, domain, kind.Label, err)
	}
	v := *u // don't mutate the server's upstream config
	u = &v
	switch u.Scheme {
	case secure:
		u.Scheme = "https"
	case plain, "http+insecure":
		u.Scheme = "http"
	}
	if u.Scheme == "" {
		// The timeout bounds a resolver that would otherwise hang startup.
		ctx, cancel := context.WithTimeout(context.Background(), alborz.RoundTripTimeout)
		defer cancel()
		s, err := kind.Discover(ctx, u.Host)
		if err != nil {
			srv.Logger().Printf("%s: domain %q: failed to discover %s server: %v", kind.Name, domain, kind.Label, err)
			return nil, nil
		}
		u, err = url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("%s: Discover returned an invalid URL: %v", kind.Name, err)
		}
	}

	srv.Logger().Printf("Domain %q: configured upstream %s server: %v", domain, kind.Label, u)
	return u, nil
}
