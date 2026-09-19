package dav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/collections"
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
	// Poll is how often a collection is asked whether it changed; see
	// davcache.DefaultPoll.
	Poll time.Duration
	// Discover finds the service's URL for a domain by its SRV record.
	Discover func(ctx context.Context, domain string) (string, error)
	// Own is the server an account names for itself, empty when it
	// names none.
	Own func(alborz.Services) string
	// Holds is the kind's collections among those kept here.
	Holds collections.Kind
	// FindHome is the home set behind the account's server. Principal
	// and home set are two round trips, asked through the kind's own
	// go-webdav client: the two share no interface to ask through.
	FindHome func(ctx context.Context, client *http.Client, endpoint string) (string, error)
	// List reads the kind's collections out of the homes.
	List func(ctx context.Context, client *http.Client, base *url.URL, homes []Home) ([]Collection, error)
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

// hereBase stands where a server's address would for an account whose
// only collections are kept here. Nothing resolves it (RFC 2606 keeps
// .invalid from ever existing): the transport answers every path under
// collections.Prefix itself.
var hereBase = &url.URL{Scheme: "http", Host: alborz.AppName + ".invalid", Path: collections.Prefix + "/"}

// WarmBudget bounds what a sign-in may spend fetching an account's
// first pages behind the scenes; a slow server leaves them cold.
const WarmBudget = 30 * time.Second

// Provider is one DAV service across the served domains: where each
// domain's server is, the cache in front of it, and the client that
// talks to it on a session's behalf.
type Provider struct {
	kind  Kind
	urls  map[string]*url.URL // endpoint per served mail domain
	cache *davcache.Cache
	// here serves the collections kept in this alborz; nil without a
	// data directory.
	here  http.Handler
	store *collections.Store
	// troubles holds, per account, what its server answered the last
	// time its homes were asked for, when that was not a home: the
	// collections kept here are listed all the same, and the page says
	// what became of the rest.
	troubles sync.Map
	// found per username; see Collections.
	collections *alborz.Memo[[]Collection]

	// Set in debug mode; logs upstream DAV traffic.
	debug echo.Logger
}

// NewProvider resolves the service for every served domain. A domain
// without one still has accounts that may name their own, so the
// provider stands either way and Enabled answers per request. The URL
// of each domain's server is found by DNS alone, so startup never waits
// on a DAV host; asking whether it answers is a probe run in the
// background, and a request surfaces an unreachable one until it does.
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
	var store *davcache.Store
	if srv.Options.CacheDir != "" && srv.Options.LoginKey != nil {
		store = davcache.NewStore(filepath.Join(srv.Options.CacheDir, kind.Name), srv.Options.LoginKey)
	}
	cache, warm, err := davcache.New(store, kind.Poll)
	if err != nil {
		return nil, fmt.Errorf("failed to load the %s cache: %w", kind.Name, err)
	}
	if warm > 0 {
		srv.Logger().Printf("%s: cache warm for %d accounts", kind.Name, warm)
	}
	p := &Provider{kind: kind, urls: urls, cache: cache,
		collections: alborz.NewBackgroundMemo[[]Collection](discoveryTTL)}
	if srv.Collections != nil {
		p.here, p.store = srv.Collections.Handler(), srv.Collections
	}
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

// URL is where the session's client starts: the account's server, or
// for an account without one the collections kept here.
func (p *Provider) URL(session *alborz.Session) (*url.URL, bool) {
	if u, ok := p.Remote(session); ok {
		return u, true
	}
	return hereBase, p.here != nil
}

// Home is one place an account's collections of the kind are listed.
type Home struct {
	Path string
	// Here marks the home kept in this alborz rather than on the
	// account's server.
	Here bool
}

// homes are the places the account's collections are listed: its
// server's home set, and the one kept here. An account has either or
// both.
func (p *Provider) homes(ctx context.Context, session *alborz.Session) ([]Home, error) {
	var homes []Home
	if u, ok := p.Remote(session); ok {
		home, err := p.kind.FindHome(ctx, p.HTTPClient(session), u.String())
		switch {
		case err == nil:
			p.troubles.Delete(session.Username())
			homes = append(homes, Home{Path: home})
		case p.here == nil:
			return nil, err
		default:
			p.troubles.Store(session.Username(), err)
		}
	}
	if p.here != nil {
		homes = append(homes, Home{Path: collections.HomePath(p.kind.Holds, session.Username()), Here: true})
	}
	return homes, nil
}

// Collections is the account's list of the kind, empty when it has
// none. Finding the homes and listing them is several round trips, so
// they are found once per user rather than on every page. The load
// outlives the request that starts it: a second page waiting on it must
// not be failed by the first one's reader going away.
func (p *Provider) Collections(ctx context.Context, session *alborz.Session) ([]Collection, error) {
	base, _ := p.URL(session)
	return p.collections.Get(session.Username(), func() ([]Collection, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryTimeout)
		defer cancel()

		homes, err := p.homes(ctx, session)
		if err != nil {
			return nil, err
		}
		infos, err := p.kind.List(ctx, p.HTTPClient(session), base, homes)
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

// Create adds a collection to the account's home, the one kept here
// when here says so, and forgets the found list, so the new one
// appears at once.
func (p *Provider) Create(ctx context.Context, session *alborz.Session, name, color string, here bool, components []string) (string, error) {
	infos, err := p.Collections(ctx, session)
	if err != nil {
		return "", err
	}
	if NameTaken(name, infos) {
		return "", ErrNameTaken
	}
	base, _ := p.URL(session)
	homes, err := p.homes(ctx, session)
	if err != nil {
		return "", err
	}
	client := p.HTTPClient(session)
	path, err := CreateCollection(ctx, base, HomeFor(homes, here), name, p.kind.Unnamed, func(ctx context.Context, target string) error {
		return p.kind.Make(ctx, client, target, name, color, components)
	})
	if err != nil {
		return "", err
	}
	p.Forget(session.Username())
	return path, nil
}

// Host names the server the account's pages talk to, which for an
// account with none elsewhere is the one the reader is looking at.
func (p *Provider) Host(ctx *alborz.Context) string {
	if u, ok := p.Remote(ctx.Session); ok {
		return u.Host
	}
	return ctx.Request().Host
}

// KeepsHere says whether collections can be kept in this alborz.
func (p *Provider) KeepsHere() bool { return p.here != nil }

// The places a new collection can go, as the create form posts them
// after the account: the account's server, or here.
const (
	placeServer = "server"
	placeHere   = "here"
)

// Places are where a new collection can go, grouped by account in the
// shape the collection picker lists: each account's server by its host,
// and its home here. An account with neither has no group.
func (p *Provider) Places(ctx *alborz.Context) []Group {
	var groups []Group
	for _, s := range ctx.Sessions() {
		var places []Collection
		if u, ok := p.Remote(s); ok {
			places = append(places, Collection{Path: placeServer, Name: u.Host})
		}
		if p.here != nil {
			places = append(places, Collection{Path: placeHere, Name: alborz.BrandName})
		}
		if len(places) > 0 {
			groups = append(groups, Group{Account: s.Username(), Collections: places})
		}
	}
	return groups
}

// ReadPlace is the place the create form chose, or where a collection
// goes when it had no choice to offer: the account's server when it has
// one, here otherwise.
func (p *Provider) ReadPlace(ctx *alborz.Context, account string) (string, bool) {
	if chosen, kind, ok := strings.Cut(ctx.FormValue("place"), "|"); ok {
		return chosen, kind == placeHere
	}
	s := ctx.SessionFor(account)
	if s == nil {
		return account, p.here != nil
	}
	_, remote := p.Remote(s)
	return account, !remote
}

// HomeFor is the home a new collection goes to: the one kept here when
// asked for, and otherwise the account's first, which is its server's
// when it has one.
func HomeFor(homes []Home, here bool) string {
	for _, home := range homes {
		if home.Here == here {
			return home.Path
		}
	}
	return homes[0].Path
}

// Trouble is what kept the account's server out of its last listing,
// nil when nothing did.
func (p *Provider) Trouble(username string) error {
	err, _ := p.troubles.Load(username)
	trouble, _ := err.(error)
	return trouble
}

// Remote resolves the account's DAV server: the one the account names
// for itself, then its domain's, then the unnamed provider's.
func (p *Provider) Remote(session *alborz.Session) (*url.URL, bool) {
	if services, err := session.Services(); err == nil {
		if own := p.kind.Own(services); own != "" {
			if u, err := url.Parse(own); err == nil {
				return u, true
			}
		}
	}
	u, ok := p.urls[session.Domain()]
	if !ok {
		u, ok = p.urls[""]
	}
	return u, ok
}

// HTTPClient talks to the session's server through the cache, with the
// account's credentials on every request.
func (p *Provider) HTTPClient(session *alborz.Session) *http.Client {
	return httpClient(p.cache, p.here, session, p.debug)
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

// HandleRefresh asks every signed-in account's server again, for a
// change made elsewhere that the poll has not caught up with, and lands
// on list.
func (p *Provider) HandleRefresh(list string) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		for _, s := range ctx.Sessions() {
			p.cache.Refresh(ctx.Request().Context(), s.Username())
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(list)))
	}
}

// Warm fetches an account's first pages behind a sign-in, so that they
// open from the cache. It outlives the request, within WarmBudget.
func (p *Provider) Warm(ctx *alborz.Context, s *alborz.Session, fetch func(context.Context) error) {
	log := ctx.Logger()
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), WarmBudget)
		defer cancel()
		if err := fetch(bg); err != nil {
			log.Printf("%s: warm %s: %v", p.kind.Name, s.Username(), err)
		}
	}()
}

// Guarded is a section's route for an account that may have none of
// the kind, which is a state to explain and not an error: one that may
// keep its first here is a form away from it, at create, and any other
// is told in words that the account has no such service.
func (p *Provider) Guarded(none error, words, create string, h func(*alborz.Context) error) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		err := h(ctx)
		if errors.Is(err, none) && p.KeepsHere() {
			return ctx.Redirect(http.StatusFound, ctx.AccountPath(create))
		}
		if errors.Is(err, none) {
			return alborz.RenderInfo(ctx, http.StatusOK, fmt.Sprintf(ctx.T(words), ctx.Session.Username()))
		}
		return err
	}
}

// HandleChoose keeps which collections of list the reader ticked, in
// the kind's own settings, and lands on the list no longer narrowed.
func HandleChoose(list, field string, keep func(store alborz.Store, paths []string) error) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		if err := keep(ctx.Session.Store(), params[field]); err != nil {
			return fmt.Errorf("failed to save the chosen collections: %w", err)
		}
		return ctx.Redirect(http.StatusFound, Widened(ctx.NextOr(list), field))
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
