package dav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
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
	// FindHome is the home set behind a source's endpoint. Principal
	// and home set are two round trips, asked through the kind's own
	// go-webdav client: the two share no interface to ask through.
	FindHome func(ctx context.Context, client *http.Client, endpoint string) (string, error)
	// List reads the kind's collections out of the homes.
	List func(ctx context.Context, client *http.Client, base *url.URL, homes []Home) ([]Collection, []error)
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
	kind Kind
	urls map[string]*url.URL // endpoint per served mail domain
	// found is, per domain, the SRV record its endpoint was found at;
	// none where the deployment named it.
	found map[string]string
	// hosts is, per domain, the DAV server on the mail host itself, where
	// one answers and it is not the domain's own (ADR 28). Filled by
	// startup probes after NewProvider returns; hostsMu guards the writes.
	hosts   map[string]*url.URL
	hostsMu sync.Mutex
	cache   *davcache.Cache
	// trusted reaches the servers the deployment names or finds; named
	// reaches the one an account names, which without the operator's
	// -private-services is out on the internet at every hop or is not
	// reached.
	trusted, named http.RoundTripper
	// here serves the collections kept in this alborz; nil without a
	// data directory.
	here  http.Handler
	store *collections.Store
	// troubles holds, per account, what each of its servers answered the
	// last time its homes were asked for, when that was not a home: the
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
	found := make(map[string]string)
	hosts := make(map[string]*url.URL)
	for _, domain := range srv.Domains() {
		u, record, err := domainURL(srv, kind, domain)
		if err != nil {
			return nil, err
		}
		if u != nil {
			urls[domain] = u
		}
		if record != "" {
			found[domain] = record
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
	p := &Provider{kind: kind, urls: urls, found: found, hosts: hosts, cache: cache,
		collections: alborz.NewBackgroundMemo[[]Collection](discoveryTTL)}
	p.trusted = cache.Limit(http.DefaultTransport)
	p.named = p.trusted
	if !srv.Options.PrivateServices {
		p.named = cache.Limit(alborz.NewRemoteTransport())
	}
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
	// until it recovers. The mail host's answer becomes a source beside
	// the domain's.
	for _, domain := range srv.Domains() {
		domain := domain
		go func() {
			// SanityCheckURL is the domain's own probe, and OnMailHost the
			// mail host's; either may be nil.
			if p.urls[domain] != nil {
				if err := SanityCheckURL(p.urls[domain]); err != nil {
					srv.Logger().Printf("Warning: %s: domain %q: %s server %q not reachable at startup: %v", kind.Name, domain, kind.Label, p.urls[domain], err)
				}
			}
			if h, ok := OnMailHost(srv.UpstreamsFor(domain).IMAP, kind.Name); ok && (p.urls[domain] == nil || h.Host != p.urls[domain].Host) {
				srv.Logger().Printf("Domain %q: %s server on the mail host: %v", domain, kind.Label, h)
				p.hostsMu.Lock()
				p.hosts[domain] = h
				p.hostsMu.Unlock()
			}
		}()
	}
	return p, nil
}

// URL is where the session's client starts: the domain's server, or
// for an account without one the address that stands for Alborz, since
// the transport routes a path by what it begins with, not by host.
func (p *Provider) URL(session *alborz.Session) (*url.URL, bool) {
	sources := p.Sources(session)
	for _, s := range sources {
		if s.ID == SourceDomain {
			return s.URL, true
		}
	}
	return hereBase, len(sources) > 0 || p.here != nil
}

// The sources an account's collections come from (ADR 28): the server
// its domain names, the one on its mail host, the one it names itself,
// and Alborz.
const (
	SourceDomain = "domain"
	SourceHost   = "host"
	SourceOwn    = "own"
	SourceHere   = "here"
)

// Source is one DAV server an account's collections can be on.
type Source struct {
	ID  string
	URL *url.URL
	// Origin is how alborz came to it, a translation key, and Record
	// the SRV record or the address that answered, where there is one.
	Origin, Record string
	// Named is the source that takes the username and password the
	// account keeps for its servers (ADR 5, 22): the server it names, or
	// its domain's when it names none. The rest take the mail login.
	Named bool
}

// Endpoint is where a client for the source starts: the domain's server
// as it is, the others under their marker, which the transport routes.
func (s Source) Endpoint() string {
	if s.ID == SourceDomain {
		return s.URL.String()
	}
	return (&url.URL{Scheme: hereBase.Scheme, Host: hereBase.Host, Path: marker(s.ID) + s.URL.Path}).String()
}

// marker begins every path on a source other than the domain's.
func marker(id string) string { return "/@" + id }

// Sources are the account's remote DAV servers, each once: the one it
// names, its domain's, and its mail host's. None replaces another.
func (p *Provider) Sources(session *alborz.Session) []Source {
	var out []Source
	if services, err := session.Services(); err == nil {
		if own := p.kind.Own(services); own != "" {
			if u, err := url.Parse(own); err == nil {
				out = append(out, Source{ID: SourceOwn, URL: u, Origin: "servers.fromaccount", Named: true})
			}
		}
	}
	domain := session.Domain()
	u, ok := p.urls[domain]
	if !ok {
		domain = ""
		u, ok = p.urls[""]
	}
	if ok {
		origin := "servers.fromconfig"
		if p.found[domain] != "" {
			origin = "servers.fromsrv"
		}
		out = append(out, Source{ID: SourceDomain, URL: u, Origin: origin, Record: p.found[domain]})
	}
	p.hostsMu.Lock()
	h, ok := p.hosts[session.Domain()]
	p.hostsMu.Unlock()
	if ok {
		out = append(out, Source{ID: SourceHost, URL: h, Origin: "servers.frommailhost", Record: h.String()})
	}
	// A server the account names that is the domain's or the mail
	// host's is that source, not another. The domain's is an address to
	// compare with, since one host can serve two servers under two
	// paths; the mail host's is only where its well-known answered.
	same := func(own, other Source) bool {
		if other.ID == SourceHost {
			return own.URL.Host == other.URL.Host
		}
		return own.URL.Scheme == other.URL.Scheme && own.URL.Host == other.URL.Host &&
			strings.TrimSuffix(own.URL.Path, "/") == strings.TrimSuffix(other.URL.Path, "/")
	}
	for i := 1; i < len(out); i++ {
		if out[0].ID == SourceOwn && same(out[0], out[i]) {
			out[i].Named = true
			out = out[1:]
			break
		}
	}
	if !slices.ContainsFunc(out, func(s Source) bool { return s.Named }) {
		for i := range out {
			out[i].Named = out[i].ID == SourceDomain
		}
	}
	return out
}

// Home is one place an account's collections of the kind are listed.
type Home struct {
	Path string
	// Source is the one the home is on, SourceHere for Alborz.
	Source string
}

// homes are the places the account's collections are listed: each
// source's home set, found from the source's endpoint, and the one kept
// here. A source that does not answer is left out and said so; the
// others stand.
func (p *Provider) homes(ctx context.Context, session *alborz.Session) ([]Home, error) {
	var homes []Home
	var failed []error
	for _, s := range p.Sources(session) {
		home, err := p.kind.FindHome(ctx, p.HTTPClient(session), s.Endpoint())
		if err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", s.URL.Host, err))
			continue
		}
		homes = append(homes, Home{Path: home, Source: s.ID})
	}
	if p.here != nil {
		homes = append(homes, Home{Path: collections.HomePath(p.kind.Holds, session.Username()), Source: SourceHere})
	}
	if len(homes) == 0 && failed != nil {
		// A refusal is the one the reader can end, so it is the one said
		// when the servers failed in different ways.
		i := max(0, slices.IndexFunc(failed, func(err error) bool {
			var no *RefusedError
			return errors.As(err, &no)
		}))
		return nil, failed[i]
	}
	p.troubles.Store(session.Username(), failed)
	return homes, nil
}

// Collections is the account's list of the kind, empty when it has
// none. Finding the homes and listing them is several round trips, so
// they are found once per user rather than on every page. The load
// outlives the request that starts it: a second page waiting on it must
// not be failed by the first one's reader going away.
func (p *Provider) Collections(ctx context.Context, session *alborz.Session) ([]Collection, error) {
	base, _ := p.URL(session)
	// A listing a server was missing from is what the others hold, not
	// the account's list: kept, but asked again once the retry delay is
	// over, or the one failure would be told on every page until a
	// restart.
	defer func() {
		if len(p.Troubles(session.Username())) > 0 {
			p.collections.Retry(session.Username())
		}
	}()
	return p.collections.Get(session.Username(), func() ([]Collection, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryTimeout)
		defer cancel()

		homes, err := p.homes(ctx, session)
		if err != nil {
			return nil, err
		}
		infos, failed := p.kind.List(ctx, p.HTTPClient(session), base, homes)
		if err := p.Settle(session.Username(), homes, failed); err != nil {
			return nil, fmt.Errorf("failed to list the %s collections: %v", p.kind.Label, err)
		}
		for i := range infos {
			infos[i].Public = infos[i].Here && p.Published(infos[i].Path)
			infos[i].SharedWith = p.SharedWith(infos[i].Path)
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

// Create adds a collection to the account's home on the place given -
// a source of the account's, or Alborz - and forgets the found list,
// so the new one appears at once.
func (p *Provider) Create(ctx context.Context, session *alborz.Session, name, color, place string, components []string) (string, error) {
	infos, err := p.Collections(ctx, session)
	if err != nil {
		return "", err
	}
	if NameTaken(name, place, infos) {
		return "", ErrNameTaken
	}
	base, _ := p.URL(session)
	homes, err := p.homes(ctx, session)
	if err != nil {
		return "", err
	}
	home, err := HomeFor(homes, place)
	if err != nil {
		return "", err
	}
	client := p.HTTPClient(session)
	path, err := CreateCollection(ctx, base, home, name, p.kind.Unnamed, func(ctx context.Context, target string) error {
		return p.kind.Make(ctx, client, target, name, color, components)
	})
	if err != nil {
		return "", err
	}
	p.Forget(session.Username())
	return path, nil
}

// originHost is the host part of where alborz is reached.
func originHost(ctx *alborz.Context) string {
	_, host, _ := strings.Cut(ctx.Origin(), "://")
	return host
}

// KeepsHere says whether collections can be kept in this alborz.
func (p *Provider) KeepsHere() bool { return p.here != nil }

// Places are where a new collection can go, grouped by account in the
// shape the collection picker lists: each of the account's sources by
// its host, and Alborz by its own. An account with none has no group.
func (p *Provider) Places(ctx *alborz.Context) []Group {
	var groups []Group
	for _, s := range ctx.Sessions() {
		var places []Collection
		for _, src := range p.Sources(s) {
			places = append(places, Collection{Path: src.ID, Name: src.URL.Host})
		}
		if p.here != nil {
			places = append(places, Collection{Path: SourceHere, Name: originHost(ctx)})
		}
		if len(places) > 0 {
			groups = append(groups, Group{Account: s.Username(), Collections: places})
		}
	}
	return groups
}

// ReadPlace is the place the create form chose, or where a collection
// goes when it had no choice to offer: the account's default when it
// has one it can still reach, else its first source, else Alborz.
func (p *Provider) ReadPlace(ctx *alborz.Context, account string) (string, string) {
	if chosen, place, ok := strings.Cut(ctx.FormValue("place"), "|"); ok {
		return chosen, place
	}
	s := ctx.SessionFor(account)
	if s == nil {
		return account, SourceHere
	}
	return account, p.DefaultPlace(s)
}

// DefaultPlace is where the account's new collections go unless the
// reader picks another.
func (p *Provider) DefaultPlace(s *alborz.Session) string {
	sources := p.Sources(s)
	if services, err := s.Services(); err == nil && services.Default != "" {
		if services.Default == SourceHere && p.here != nil {
			return SourceHere
		}
		for _, src := range sources {
			if src.ID == services.Default {
				return src.ID
			}
		}
	}
	if len(sources) > 0 {
		return sources[0].ID
	}
	return SourceHere
}

// ErrPlaceDown refuses a new collection whose place has no home to
// give: its server did not answer, or the account has no such place.
// Any other home would put the collection where nobody asked for it.
var ErrPlaceDown = errors.New("the place asked for has no home")

// HomeFor is the home a new collection goes to: the one on the place
// asked for.
func HomeFor(homes []Home, place string) (string, error) {
	for _, home := range homes {
		if home.Source == place {
			return home.Path, nil
		}
	}
	return "", ErrPlaceDown
}

// Settle is what a listing of the account's homes came to: an error
// when none of them answered, and otherwise nothing, with the ones that
// failed kept beside what their servers said when the homes were found.
func (p *Provider) Settle(username string, homes []Home, failed []error) error {
	if len(failed) > 0 && len(failed) == len(homes) {
		return failed[0]
	}
	if len(failed) > 0 {
		p.troubles.Store(username, slices.Concat(p.Troubles(username), failed))
	}
	return nil
}

// Troubles are what kept each of the account's servers out of its last
// listing, none when nothing did.
func (p *Provider) Troubles(username string) []error {
	failed, _ := p.troubles.Load(username)
	troubles, _ := failed.([]error)
	return troubles
}

// HTTPClient talks to the session's server through the cache, with the
// account's credentials on every request.
func (p *Provider) HTTPClient(session *alborz.Session) *http.Client {
	return httpClient(p, session)
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
func domainURL(srv *alborz.Server, kind Kind, domain string) (*url.URL, string, error) {
	secure, plain := kind.Schemes[0], kind.Schemes[1]
	record := ""
	u, err := srv.Upstream(domain, secure, plain, "https", "http+insecure")
	if _, ok := err.(*alborz.NoUpstreamError); ok {
		return nil, "", nil
	} else if err != nil {
		return nil, "", fmt.Errorf("%s: domain %q: failed to parse upstream %s server: %v", kind.Name, domain, kind.Label, err)
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
			return nil, "", nil
		}
		u, err = url.Parse(s)
		if err != nil {
			return nil, "", fmt.Errorf("%s: Discover returned an invalid URL: %v", kind.Name, err)
		}
		// go-webdav asks the TLS record first (RFC 6764 3), and the
		// scheme it answers says which one it found.
		service := kind.Name
		if u.Scheme == "https" {
			service += "s"
		}
		record = "_" + service + "._tcp." + domain
	}

	srv.Logger().Printf("Domain %q: configured upstream %s server: %v", domain, kind.Label, u)
	return u, record, nil
}
