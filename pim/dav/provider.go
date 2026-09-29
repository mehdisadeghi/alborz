package dav

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/memo"
	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/web"
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
	Own func(Services) string
	// Holds is the kind's collections among those kept here.
	Holds store.Kind
	// FindHome is the home set behind a source's endpoint, asked through the kind's own client.
	FindHome func(ctx context.Context, client *http.Client, endpoint string) (string, error)
	// List reads the kind's collections out of the homes.
	List func(ctx context.Context, client *http.Client, base *url.URL, homes []Home) ([]Collection, []error)
	// Make makes a collection at path; an address book gets no components.
	Make func(ctx context.Context, client *http.Client, base *url.URL, path, name, color string, components []string) error
	// Change renames and recolours the collection at path.
	Change func(ctx context.Context, client *http.Client, base *url.URL, path, name, color string) error
	// Unnamed is the address of a new collection whose name gives none.
	Unnamed string
}

// discoveryTTL is when a served collection list is reloaded in the background.
const discoveryTTL = 5 * time.Minute

// The load is detached from its requester, so only its own deadline stops a hung server wedging every waiter.
const discoveryTimeout = 30 * time.Second

// base is where every client starts; the transport routes a path by its marker, never by this host,
// which never resolves (RFC 2606).
var base = &url.URL{Scheme: "http", Host: alborz.AppName + ".invalid", Path: "/"}

// Base is the URL a client of any account's collections is made with.
func Base() *url.URL {
	u := *base
	return &u
}

// WarmBudget bounds a sign-in's background fetch of an account's first pages.
const WarmBudget = 30 * time.Second

// Provider is one DAV service across the served domains.
type Provider struct {
	Kind Kind
	urls map[string]*url.URL
	// found is, per domain, the SRV record its endpoint came from; none where the deployment named it.
	found map[string]string
	// hosts is, per domain, a DAV server on the mail host that is not the domain's own (ADR 28).
	// Startup probes fill it after NewProvider returns; hostsMu guards the writes.
	hosts   map[string]*url.URL
	hostsMu sync.Mutex
	// instance is this alborz's name; selves holds, per domain server, whether it is this alborz.
	instance string
	selves   sync.Map
	// mirror is nil with caching off.
	mirror *Mirror
	// trusted reaches the servers the deployment names or finds; named reaches the one an account names,
	// which without -private-services stays on the public internet at every hop.
	trusted, named http.RoundTripper
	// here is this alborz as a source, which local answers in-process.
	here  Source
	local http.Handler
	Store *store.Store
	// troubles holds, per account, what its servers answered when last asked for homes, when not a home.
	troubles sync.Map
	// found per username; see Collections.
	collections *memo.Memo[[]Collection]

	// debug logs upstream DAV traffic in debug mode.
	debug *log.Logger
}

// NewProvider resolves the service for every served domain by DNS alone; probes run in the background.
func NewProvider(srv *web.Server, st *store.Store, kind Kind) (*Provider, error) {
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
	var mirror *Mirror
	if srv.Options.Cache && srv.Options.CacheDir != "" {
		m, err := OpenMirror(srv.Options.CacheDir, kind, srv.Options.LoginKey)
		if err != nil {
			return nil, err
		}
		mirror = m
		srv.Schedule.Add(tick, m.round)
	}
	here, err := hereSource(srv.Options.PublicURL)
	if err != nil {
		return nil, err
	}
	p := &Provider{Kind: kind, urls: urls, found: found, hosts: hosts, mirror: mirror,
		collections: memo.NewBackground[[]Collection](srv.Options.CacheFor(discoveryTTL))}
	p.trusted, p.named = srv.Outgoing.Trusted, srv.Outgoing.Trusted
	if !srv.Options.PrivateServices {
		p.named = srv.Outgoing.Guarded
	}
	p.here, p.local, p.Store = here, st.Handler(), st
	st.OnChange(func(accounts []string) {
		for _, account := range accounts {
			p.Forget(account)
		}
	})
	p.instance = srv.Instance
	if srv.Options.Debug {
		p.debug = srv.Logger()
	}
	srv.OnAccountGone = append(srv.OnAccountGone, func(username string) {
		p.mirror.Forget(username)
		p.Forget(username)
		p.troubles.Delete(username)
	})
	srv.OnClose = append(srv.OnClose, p.Close)

	// Probes may take seconds each, so they run after the port is open.
	for _, domain := range srv.Domains() {
		domain := domain
		go func() {
			if p.urls[domain] != nil {
				if err := SanityCheckURL(srv.Outgoing.Trusted, p.urls[domain]); err != nil {
					srv.Logger().Printf("Warning: %s: domain %q: %s server %q not reachable at startup: %v", kind.Name, domain, kind.Label, p.urls[domain], err)
				}
			}
			if h, ok := OnMailHost(srv.Outgoing.Trusted, srv.UpstreamsFor(domain).IMAP, kind.Name, srv.Instance); ok && (p.urls[domain] == nil || h.Host != p.urls[domain].Host) {
				srv.Logger().Printf("Domain %q: %s server on the mail host: %v", domain, kind.Label, h)
				p.hostsMu.Lock()
				p.hosts[domain] = h
				p.hostsMu.Unlock()
			}
		}()
	}
	return p, nil
}

// hereSource is this alborz at its public address, else at its machine's name: either tells it
// from another alborz whose collections an account's shared settings may name.
func hereSource(public *url.URL) (Source, error) {
	u := &url.URL{Scheme: base.Scheme, Path: store.Prefix + "/"}
	if public != nil {
		u.Scheme, u.Host = public.Scheme, public.Host
	} else {
		host, err := os.Hostname()
		if err != nil {
			return Source{}, fmt.Errorf("without -public-url alborz names its collections by the machine's name: %w", err)
		}
		u.Host = strings.ToLower(host)
	}
	return Source{ID: SourceHere, URL: u, Origin: "servers.onalborz"}, nil
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

// Endpoint is where a client for the source starts.
func (s Source) Endpoint() string {
	return (&url.URL{Scheme: base.Scheme, Host: base.Host, Path: s.mark(s.URL.Path)}).String()
}

// marker begins every path alborz holds: the server's host, so paths of two servers never meet (ADR 28).
func marker(host string) string { return "/@" + host }

// mark is a path on the source as alborz holds it.
func (s Source) mark(p string) string { return marker(s.URL.Host) + p }

// held reads a path on this alborz as the store holds it: the collection, and the home it is reached through.
func (s Source) held(p string) (store.Ref, string, bool) {
	rest, ok := strings.CutPrefix(p, marker(s.URL.Host))
	if !ok || s.ID != SourceHere {
		return store.Ref{}, "", false
	}
	return store.Held(rest)
}

// route is the source a marked path is on, and the path there: the host picks, the longest base path
// settles two sources on one host, and a path under neither goes to the first.
func route(sources []Source, p string) (Source, string, error) {
	rest, ok := strings.CutPrefix(p, "/@")
	if !ok {
		return Source{}, "", fmt.Errorf("dav: %q names no server", p)
	}
	host, rest, _ := strings.Cut(rest, "/")
	rest = "/" + rest
	var found Source
	longest := -1
	for _, s := range sources {
		if s.URL.Host != host {
			continue
		}
		n := 0
		if strings.HasPrefix(rest, s.URL.Path) {
			n = len(s.URL.Path)
		}
		if n > longest {
			found, longest = s, n
		}
	}
	if longest < 0 {
		return Source{}, "", fmt.Errorf("dav: %s is none of this account's servers", host)
	}
	return found, rest, nil
}

// Address is where a path alborz holds is on its server, as another client reaches it.
func (p *Provider) Address(ctx context.Context, login *alborz.Login, held string) (string, error) {
	src, rest, err := route(p.Sources(ctx, login), held)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: src.URL.Scheme, Host: src.URL.Host, Path: rest}).String(), nil
}

// isHere says whether the server at u is this alborz. It is asked once, on first use rather than at
// start: a request is being served by then, so this alborz answers.
func (p *Provider) isHere(u *url.URL) bool {
	once, _ := p.selves.LoadOrStore(u.String(), sync.OnceValue(func() bool { return answersAs(p.trusted, u, p.instance) }))
	return once.(func() bool)()
}

// Sources are the account's DAV servers, each once: its own, its domain's, its mail host's and this alborz.
func (p *Provider) Sources(ctx context.Context, login *alborz.Login) []Source {
	var out []Source
	if services, err := LoadServices(ctx, login.Store()); err == nil {
		if own := p.Kind.Own(services); own != "" {
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
	if ok && !p.isHere(u) {
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
	return append(out, p.here)
}

// Home is one place an account's collections of the kind are listed.
type Home struct {
	Path   string
	Source Source
}

// homes are each source's home set; a source that does not answer is left out and said so.
func (p *Provider) homes(ctx context.Context, login *alborz.Login) []Home {
	var homes []Home
	var failed []error
	for _, s := range p.Sources(ctx, login) {
		home, err := p.Kind.FindHome(ctx, p.HTTPClient(ctx, login), s.Endpoint())
		if err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", s.URL.Host, err))
			continue
		}
		homes = append(homes, Home{Path: home, Source: s})
	}
	p.troubles.Store(login.Username(), failed)
	return homes
}

// Collections is the account's list of the kind, found once per user; the load outlives the request that starts it.
func (p *Provider) Collections(ctx context.Context, login *alborz.Login) ([]Collection, error) {
	// A listing missing a server is kept but asked again after the retry delay, or the failure would stay until restart.
	defer func() {
		if len(p.Troubles(login.Username())) > 0 {
			p.collections.Retry(strings.ToLower(login.Username()))
		}
	}()
	return p.collections.Get(strings.ToLower(login.Username()), func() ([]Collection, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryTimeout)
		defer cancel()

		homes := p.homes(ctx, login)
		infos, failed := p.Kind.List(ctx, p.HTTPClient(ctx, login), Base(), homes)
		if err := p.Settle(login.Username(), homes, failed); err != nil {
			return nil, fmt.Errorf("failed to list the %s collections: %v", p.Kind.Label, err)
		}
		for i := range infos {
			infos[i].Public = infos[i].Here && p.Published(infos[i].Path)
			infos[i].SharedWith = p.SharedWith(infos[i].Path)
			infos[i].Feed = p.Feed(infos[i].Path)
		}
		return infos, nil
	})
}

// Forget drops the account's found list, for a change the next page has to show. The store names
// accounts in lower case, so the list is kept by that name.
func (p *Provider) Forget(username string) { p.collections.Forget(strings.ToLower(username)) }

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
	home, err := HomeFor(p.homes(ctx, login), place)
	if err != nil {
		return "", err
	}
	client := p.HTTPClient(ctx, login)
	path, err := CreateCollection(ctx, home, name, p.Kind.Unnamed, func(ctx context.Context, path string) error {
		return p.Kind.Make(ctx, client, Base(), path, name, color, components)
	})
	if err != nil {
		return "", err
	}
	p.Forget(login.Username())
	return path, nil
}

// placeHost is the host a reader knows a source by; alborz knows its own from -public-url, else from the request.
func placeHost(ctx *web.Context, src Source) string {
	if src.ID != SourceHere {
		return src.URL.Host
	}
	_, host, _ := strings.Cut(ctx.Origin(), "://")
	return host
}

// PlaceLabel says what a place is rather than where: a host name alone tells the reader nothing.
func PlaceLabel(ctx *web.Context, id, host string) string {
	label := ctx.T("place." + id)
	if host == "" {
		return label
	}
	return label + " · " + host
}

// Group is one account's collections, for a form's destination.
type Group struct {
	Account     string
	Collections []Collection
}

// Places are where a new collection can go, grouped by account.
func (p *Provider) Places(ctx *web.Context) []Group {
	var groups []Group
	for _, s := range ctx.Logins() {
		var places []Collection
		for _, src := range p.Sources(ctx.Request().Context(), s) {
			places = append(places, Collection{Path: src.ID, Name: PlaceLabel(ctx, src.ID, placeHost(ctx, src))})
		}
		groups = append(groups, Group{Account: s.Username(), Collections: places})
	}
	return groups
}

// ReadPlace is the form's chosen place, else the account's reachable default, its first source, or Alborz.
func (p *Provider) ReadPlace(ctx *web.Context, account string) (string, string) {
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
	if services, err := LoadServices(ctx, s.Store()); err == nil && services.Default != "" {
		for _, src := range sources {
			if src.ID == services.Default {
				return src.ID
			}
		}
	}
	return sources[0].ID
}

// ErrPlaceDown refuses a new collection whose place has no home; any other home would be unasked for.
var ErrPlaceDown = errors.New("the place asked for has no home")

// HomeFor is the home on the place asked for.
func HomeFor(homes []Home, place string) (string, error) {
	for _, home := range homes {
		if home.Source.ID == place {
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
func (p *Provider) CountObjects(ctx *web.Context, coll string) func() int {
	return func() int {
		n, err := CountObjects(ctx.Request().Context(), p.HTTPClient(ctx.Request().Context(), ctx.Login), Base(), coll)
		if err != nil {
			ctx.Logger().Printf("%s: failed to count %s: %v", p.Kind.Name, coll, err)
			return -1
		}
		return n
	}
}

// HandleRefresh asks every signed-in account's server again and lands on list.
func (p *Provider) HandleRefresh(list string) func(*web.Context) error {
	return func(ctx *web.Context) error {
		for _, s := range ctx.Logins() {
			rc := ctx.Request().Context()
			who, err := principal(rc, s)
			if err == nil {
				err = p.mirror.Refresh(rc, who, p.router(p.Sources(rc, s), s))
			}
			if err != nil {
				ctx.Logger().Printf("%s: refresh %s: %v", p.Kind.Name, s.Username(), err)
			}
		}
		// The list itself is what was asked for.
		ctx.Quiet()
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath(list)))
	}
}

// Warm fetches an account's first pages behind a sign-in so they open from the mirror, within WarmBudget.
func (p *Provider) Warm(ctx *web.Context, s *alborz.Login, fetch func(context.Context) error) {
	if p.mirror == nil {
		return
	}
	log := ctx.Logger()
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), WarmBudget)
		defer cancel()
		if err := fetch(bg); err != nil {
			log.Printf("%s: warm %s: %v", p.Kind.Name, s.Username(), err)
		}
	}()
}

// Guarded sends an account with none of the kind to create its first here instead of failing.
func (p *Provider) Guarded(none error, create string, h func(*web.Context) error) func(*web.Context) error {
	return func(ctx *web.Context) error {
		if err := h(ctx); !errors.Is(err, none) {
			return err
		}
		return ctx.Redirect(http.StatusFound, ctx.AccountPath(create))
	}
}

// Close waits for the mirror's syncs and closes its file.
func (p *Provider) Close() error { return p.mirror.Close() }

// domainURL resolves the domain's endpoint from DNS and config only; nil without error means none.
func domainURL(srv *web.Server, kind Kind, domain string) (*url.URL, string, error) {
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
