package dav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/collections"
	"github.com/labstack/echo/v4"
)

// Kind tells a CalDAV provider from a CardDAV one.
type Kind struct {
	// Name is the plugin's, for the log; Label is what a reader calls the service.
	Name, Label string
	// Schemes are the explicit upstream schemes, secure and plain.
	Schemes [2]string
	// Poll is how often a collection is asked whether it changed, since CalDAV and CardDAV push nothing.
	Poll time.Duration
	// Discover finds the service's URL for a domain by its SRV record.
	Discover func(ctx context.Context, domain string) (string, error)
	// Own is the server an account names for itself, empty when none.
	Own func(alborz.Services) string
	// Holds is the kind's collections among those kept here.
	Holds collections.Kind
	// FindHome is the home set behind a source's endpoint, asked through the kind's own client.
	FindHome func(ctx context.Context, client *http.Client, endpoint string) (string, error)
	// List reads the kind's collections out of the homes.
	List func(ctx context.Context, client *http.Client, base *url.URL, homes []Home) ([]Collection, []error)
	// Make makes a collection at target; an address book gets no components.
	Make func(ctx context.Context, client *http.Client, target, name, color string, components []string) error
	// Unnamed is the address of a new collection whose name gives none.
	Unnamed string
}

// discoveryTTL is when a served collection list is reloaded in the background.
const discoveryTTL = 5 * time.Minute

// The load is detached from its requester, so only its own deadline stops a hung server wedging every waiter.
const discoveryTimeout = 30 * time.Second

// hereBase stands for an account whose collections are all kept here; .invalid never resolves (RFC 2606).
var hereBase = &url.URL{Scheme: "http", Host: alborz.AppName + ".invalid", Path: collections.Prefix + "/"}

// WarmBudget bounds a sign-in's background fetch of an account's first pages.
const WarmBudget = 30 * time.Second

// Provider is one DAV service across the served domains.
type Provider struct {
	kind Kind
	urls map[string]*url.URL
	// found is, per domain, the SRV record its endpoint came from; none where the deployment named it.
	found map[string]string
	// hosts is, per domain, a DAV server on the mail host that is not the domain's own (ADR 28).
	// Startup probes fill it after NewProvider returns; hostsMu guards the writes.
	hosts   map[string]*url.URL
	hostsMu sync.Mutex
	// mirror is nil with caching off.
	mirror *Mirror
	// trusted reaches the servers the deployment names or finds; named reaches the one an account names,
	// which without -private-services stays on the public internet at every hop.
	trusted, named http.RoundTripper
	// here serves the collections kept in this alborz.
	here  http.Handler
	store *collections.Store
	// troubles holds, per account, what its servers answered when last asked for homes, when not a home.
	troubles sync.Map
	// found per username; see Collections.
	collections *alborz.Memo[[]Collection]

	// debug logs upstream DAV traffic in debug mode.
	debug echo.Logger
}

// NewProvider resolves the service for every served domain by DNS alone; probes run in the background.
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
	// The copies are sealed under the login key, so without one there
	// are none.
	var mirror *Mirror
	if srv.Options.Cache && srv.Options.CacheDir != "" && srv.Options.LoginKey != nil {
		m, err := OpenMirror(srv.Options.CacheDir, kind, srv.Options.LoginKey)
		if err != nil {
			return nil, err
		}
		mirror = m
	}
	p := &Provider{kind: kind, urls: urls, found: found, hosts: hosts, mirror: mirror,
		collections: alborz.NewBackgroundMemo[[]Collection](srv.Options.CacheFor(discoveryTTL))}
	p.trusted, p.named = srv.Outgoing.Trusted, srv.Outgoing.Trusted
	if !srv.Options.PrivateServices {
		p.named = srv.Outgoing.Guarded
	}
	p.here, p.store = srv.Collections.Handler(), srv.Collections
	if srv.Options.Debug {
		p.debug = srv.Logger()
	}
	srv.OnAccountGone = append(srv.OnAccountGone, func(username string) {
		if err := p.mirror.Forget(username); err != nil {
			srv.Logger().Printf("%s: forget %s: %v", kind.Name, username, err)
		}
		p.collections.Forget(username)
		p.troubles.Delete(username)
	})

	// Probes may take seconds each, so they run after the port is open.
	for _, domain := range srv.Domains() {
		domain := domain
		go func() {
			if p.urls[domain] != nil {
				if err := SanityCheckURL(srv.Outgoing.Trusted, p.urls[domain]); err != nil {
					srv.Logger().Printf("Warning: %s: domain %q: %s server %q not reachable at startup: %v", kind.Name, domain, kind.Label, p.urls[domain], err)
				}
			}
			if h, ok := OnMailHost(srv.Outgoing.Trusted, srv.UpstreamsFor(domain).IMAP, kind.Name); ok && (p.urls[domain] == nil || h.Host != p.urls[domain].Host) {
				srv.Logger().Printf("Domain %q: %s server on the mail host: %v", domain, kind.Label, h)
				p.hostsMu.Lock()
				p.hosts[domain] = h
				p.hostsMu.Unlock()
			}
		}()
	}
	return p, nil
}

// URL is where the login's client starts; the transport routes by path, not host, so hereBase serves an account without a server.
func (p *Provider) URL(ctx context.Context, login *alborz.Login) *url.URL {
	for _, s := range p.Sources(ctx, login) {
		if s.ID == SourceDomain {
			return s.URL
		}
	}
	return hereBase
}

// The sources an account's collections come from (ADR 28).
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
	// Origin is how alborz came to it, a translation key; Record the SRV record or answering address.
	Origin, Record string
	// Named is the source that takes the account's own server credentials (ADR 5, 22); the rest take the mail login.
	Named bool
}

// Endpoint is where a client for the source starts; sources other than the domain's sit under their marker.
func (s Source) Endpoint() string {
	if s.ID == SourceDomain {
		return s.URL.String()
	}
	return (&url.URL{Scheme: hereBase.Scheme, Host: hereBase.Host, Path: marker(s.ID) + s.URL.Path}).String()
}

// marker begins every path on a source other than the domain's.
func marker(id string) string { return "/@" + id }

// Sources are the account's remote DAV servers, each once: its own, its domain's and its mail host's.
func (p *Provider) Sources(ctx context.Context, login *alborz.Login) []Source {
	var out []Source
	if services, err := login.Services(ctx); err == nil {
		if own := p.kind.Own(services); own != "" {
			if u, err := url.Parse(own); err == nil {
				out = append(out, Source{ID: SourceOwn, URL: u, Origin: "servers.fromaccount", Named: true})
			}
		}
	}
	domain := login.Domain()
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
	h, ok := p.hosts[login.Domain()]
	p.hostsMu.Unlock()
	if ok {
		out = append(out, Source{ID: SourceHost, URL: h, Origin: "servers.frommailhost", Record: h.String()})
	}
	// A named server that is the domain's or mail host's is that source; one host may serve two servers under two paths.
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

// homes are each source's home set and the one kept here; a source that does not answer is left out and said so.
func (p *Provider) homes(ctx context.Context, login *alborz.Login) []Home {
	var homes []Home
	var failed []error
	for _, s := range p.Sources(ctx, login) {
		home, err := p.kind.FindHome(ctx, p.HTTPClient(ctx, login), s.Endpoint())
		if err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", s.URL.Host, err))
			continue
		}
		homes = append(homes, Home{Path: home, Source: s.ID})
	}
	p.troubles.Store(login.Username(), failed)
	return append(homes, Home{Path: collections.HomePath(p.kind.Holds, login.Username()), Source: SourceHere})
}

// Collections is the account's list of the kind, found once per user; the load outlives the request that starts it.
func (p *Provider) Collections(ctx context.Context, login *alborz.Login) ([]Collection, error) {
	base := p.URL(ctx, login)
	// A listing missing a server is kept but asked again after the retry delay, or the failure would stay until restart.
	defer func() {
		if len(p.Troubles(login.Username())) > 0 {
			p.collections.Retry(login.Username())
		}
	}()
	return p.collections.Get(login.Username(), func() ([]Collection, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryTimeout)
		defer cancel()

		homes := p.homes(ctx, login)
		infos, failed := p.kind.List(ctx, p.HTTPClient(ctx, login), base, homes)
		if err := p.Settle(login.Username(), homes, failed); err != nil {
			return nil, fmt.Errorf("failed to list the %s collections: %v", p.kind.Label, err)
		}
		for i := range infos {
			infos[i].Public = infos[i].Here && p.Published(infos[i].Path)
			infos[i].SharedWith = p.SharedWith(infos[i].Path)
		}
		return infos, nil
	})
}

// Forget drops the account's found list, for a change the next page has to show.
func (p *Provider) Forget(username string) { p.collections.Forget(username) }

// Opened is the login's client of the kind with the account's collections.
func Opened[C any](ctx context.Context, p *Provider, login *alborz.Login, client func(context.Context, *alborz.Login) (C, error)) (C, []Collection, error) {
	c, err := client(ctx, login)
	if err != nil {
		return c, nil, err
	}
	infos, err := p.Collections(ctx, login)
	return c, infos, err
}

// Create adds a collection to the account's home on place and forgets the found list.
func (p *Provider) Create(ctx context.Context, login *alborz.Login, name, color, place string, components []string) (string, error) {
	infos, err := p.Collections(ctx, login)
	if err != nil {
		return "", err
	}
	if NameTaken(name, place, infos) {
		return "", ErrNameTaken
	}
	base := p.URL(ctx, login)
	home, err := HomeFor(p.homes(ctx, login), place)
	if err != nil {
		return "", err
	}
	client := p.HTTPClient(ctx, login)
	path, err := CreateCollection(ctx, base, home, name, p.kind.Unnamed, func(ctx context.Context, target string) error {
		return p.kind.Make(ctx, client, target, name, color, components)
	})
	if err != nil {
		return "", err
	}
	p.Forget(login.Username())
	return path, nil
}

func originHost(ctx *alborz.Context) string {
	_, host, _ := strings.Cut(ctx.Origin(), "://")
	return host
}

// PlaceLabel says what a place is rather than where: a host name alone tells the reader nothing.
func PlaceLabel(ctx *alborz.Context, id, host string) string {
	label := ctx.T("place." + id)
	if host == "" {
		return label
	}
	return label + " · " + host
}

// Places are where a new collection can go, grouped by account.
func (p *Provider) Places(ctx *alborz.Context) []Group {
	var groups []Group
	for _, s := range ctx.Logins() {
		var places []Collection
		for _, src := range p.Sources(ctx.Request().Context(), s) {
			places = append(places, Collection{Path: src.ID, Name: PlaceLabel(ctx, src.ID, src.URL.Host)})
		}
		places = append(places, Collection{Path: SourceHere, Name: PlaceLabel(ctx, SourceHere, originHost(ctx))})
		groups = append(groups, Group{Account: s.Username(), Collections: places})
	}
	return groups
}

// ReadPlace is the form's chosen place, else the account's reachable default, its first source, or Alborz.
func (p *Provider) ReadPlace(ctx *alborz.Context, account string) (string, string) {
	// Account and place are two fields; one holding both read as servers with accounts mixed through.
	if chosen := ctx.FormValue("account"); chosen != "" && ctx.LoginFor(chosen) != nil {
		account = chosen
	}
	if place := ctx.FormValue("place"); place != "" {
		return account, place
	}
	// The account in view is in force; otherwise the session's first would be preselected.
	if scoped := ctx.URLAccount(); scoped != "" {
		account = scoped
	}
	s := ctx.LoginFor(account)
	if s == nil {
		return account, SourceHere
	}
	return account, p.DefaultPlace(ctx.Request().Context(), s)
}

// DefaultPlace is where the account's new collections go unless the reader picks another.
func (p *Provider) DefaultPlace(ctx context.Context, s *alborz.Login) string {
	sources := p.Sources(ctx, s)
	if services, err := s.Services(ctx); err == nil && services.Default != "" {
		if services.Default == SourceHere {
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

// ErrPlaceDown refuses a new collection whose place has no home; any other home would be unasked for.
var ErrPlaceDown = errors.New("the place asked for has no home")

// HomeFor is the home on the place asked for.
func HomeFor(homes []Home, place string) (string, error) {
	for _, home := range homes {
		if home.Source == place {
			return home.Path, nil
		}
	}
	return "", ErrPlaceDown
}

// Settle errors when no home answered, and otherwise records the failed ones as troubles.
func (p *Provider) Settle(username string, homes []Home, failed []error) error {
	if len(failed) > 0 && len(failed) == len(homes) {
		return failed[0]
	}
	if len(failed) > 0 {
		p.troubles.Store(username, slices.Concat(p.Troubles(username), failed))
	}
	return nil
}

// Troubles are what kept each of the account's servers out of its last listing.
func (p *Provider) Troubles(username string) []error {
	failed, _ := p.troubles.Load(username)
	troubles, _ := failed.([]error)
	return troubles
}

// HTTPClient talks to the login's server through the mirror, with the account's credentials.
func (p *Provider) HTTPClient(ctx context.Context, login *alborz.Login) *http.Client {
	return httpClient(ctx, p, login)
}

// CountObjects counts a collection's objects lazily; -1 means the server did not answer.
func (p *Provider) CountObjects(ctx *alborz.Context, coll string) func() int {
	return func() int {
		base := p.URL(ctx.Request().Context(), ctx.Login)
		n, err := CountObjects(ctx.Request().Context(), p.HTTPClient(ctx.Request().Context(), ctx.Login), base, coll)
		if err != nil {
			ctx.Logger().Printf("%s: failed to count %s: %v", p.kind.Name, coll, err)
			return -1
		}
		return n
	}
}

// HandleRefresh asks every signed-in account's server again and lands on list.
func (p *Provider) HandleRefresh(list string) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		for _, s := range ctx.Logins() {
			rc := ctx.Request().Context()
			if err := p.mirror.Refresh(rc, s.Username(), p.router(rc, s)); err != nil {
				ctx.Logger().Printf("%s: refresh %s: %v", p.kind.Name, s.Username(), err)
			}
		}
		// The list itself is what was asked for.
		ctx.Quiet()
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(list)))
	}
}

// Warm fetches an account's first pages behind a sign-in so they open from the mirror, within WarmBudget.
func (p *Provider) Warm(ctx *alborz.Context, s *alborz.Login, fetch func(context.Context) error) {
	if p.mirror == nil {
		return
	}
	log := ctx.Logger()
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), WarmBudget)
		defer cancel()
		if err := fetch(bg); err != nil {
			log.Printf("%s: warm %s: %v", p.kind.Name, s.Username(), err)
		}
	}()
}

// Guarded sends an account with none of the kind to create its first here instead of failing.
func (p *Provider) Guarded(none error, create string, h func(*alborz.Context) error) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		if err := h(ctx); !errors.Is(err, none) {
			return err
		}
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(create))
	}
}

// HandleChoose keeps which collections of list the reader ticked and lands on the list, no longer narrowed.
func HandleChoose(list, field string, keep func(ctx context.Context, store alborz.Store, paths []string) error) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		if err := keep(ctx.Request().Context(), ctx.Login.Store(), params[field]); err != nil {
			ctx.Notify(Refused(ctx, err))
		} else {
			// The list comes back holding what was chosen.
			ctx.Quiet()
		}
		return ctx.Redirect(http.StatusFound, Widened(ctx.NextOr(list), field))
	}
}

// Close stops the mirror's loop.
func (p *Provider) Close() error { return p.mirror.Close() }

// domainURL resolves the domain's endpoint from DNS and config only; nil without error means none.
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
		service := kind.Name
		if u.Scheme == "https" {
			service += "s"
		}
		record = "_" + service + "._tcp." + domain
	}

	srv.Logger().Printf("Domain %q: configured upstream %s server: %v", domain, kind.Label, u)
	return u, record, nil
}
