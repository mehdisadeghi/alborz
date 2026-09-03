package dav

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

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
}

// Provider is one DAV service across the served domains: where each
// domain's server is, the cache in front of it, and the client that
// talks to it on a session's behalf.
type Provider struct {
	kind  Kind
	urls  map[string]*url.URL // endpoint per served mail domain
	cache *davcache.Cache

	// Set in debug mode; logs upstream DAV traffic.
	debug echo.Logger
}

// NewProvider resolves the service for every served domain. Nil without
// an error means no domain has it, and the plugin has nothing to serve.
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
	p := &Provider{kind: kind, urls: urls, cache: davcache.New()}
	if srv.Options.Debug {
		p.debug = srv.Logger()
	}
	p.cache.Start()
	// Signing out is the only thing that ends the cache's authority to
	// hold this account's collections; idleness no longer does.
	srv.OnAccountGone = append(srv.OnAccountGone, p.cache.Forget)
	return p, nil
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

// Close stops the cache's refresh loop.
func (p *Provider) Close() error {
	p.cache.Stop()
	return nil
}

// domainURL resolves the domain's endpoint; nil without error means the
// domain has none.
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
		// Startup must not hang on a DAV host that swallows packets.
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

	// An unreachable DAV server must not keep the whole webmail from
	// starting; requests surface the failure until it recovers.
	if err := SanityCheckURL(u); err != nil {
		srv.Logger().Printf("Warning: %s: domain %q: %s server %q not reachable at startup: %v", kind.Name, domain, kind.Label, u, err)
	}

	srv.Logger().Printf("Domain %q: configured upstream %s server: %v", domain, kind.Label, u)
	return u, nil
}
