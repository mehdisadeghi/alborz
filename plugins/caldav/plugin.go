package alpscaldav

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"

	"git.sr.ht/~migadu/alps"
	"git.sr.ht/~migadu/alps/plugins/davcache"
	"github.com/emersion/go-webdav/caldav"
)

const (
	inputDateLayout     = "2006-01-02"
	inputDateTimeLayout = "2006-01-02T15:04"
)

type plugin struct {
	alps.GoPlugin
	urls  map[string]*url.URL // CalDAV endpoint per served mail domain
	cache *davcache.Cache

	jarsMu sync.Mutex
	jars   map[string]http.CookieJar // per username
}

func (p *plugin) client(session *alps.Session) (*caldav.Client, error) {
	u, ok := p.davURL(session)
	if !ok {
		return nil, errNoCalendar
	}
	return newClient(u, p.httpClient(session))
}

// jar returns the user's persistent cookie jar. Reusing the DAV server's
// session cookie lets it skip re-authenticating every single request.
func (p *plugin) jar(session *alps.Session) http.CookieJar {
	p.jarsMu.Lock()
	defer p.jarsMu.Unlock()
	j, ok := p.jars[session.Username()]
	if !ok {
		var err error
		j, err = cookiejar.New(nil)
		if err != nil {
			panic(err) // cannot happen with nil options
		}
		p.jars[session.Username()] = j
	}
	return j
}

// davURL resolves the session's CalDAV endpoint, falling back to the
// unnamed provider's.
func (p *plugin) davURL(session *alps.Session) (*url.URL, bool) {
	u, ok := p.urls[session.Domain()]
	if !ok {
		u, ok = p.urls[""]
	}
	return u, ok
}

func sanityCheckURL(u *url.URL) error {
	req, err := http.NewRequest(http.MethodOptions, u.String(), nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("HTTP request failed: %v %v", resp.StatusCode, resp.Status)
	}
	return nil
}

// domainURL resolves the domain's CalDAV endpoint; nil without error means
// the domain has none.
func domainURL(srv *alps.Server, domain string) (*url.URL, error) {
	u, err := srv.Upstream(domain, "caldavs", "caldav+insecure", "https", "http+insecure")
	if _, ok := err.(*alps.NoUpstreamError); ok {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("caldav: domain %q: failed to parse upstream caldav server: %v", domain, err)
	}
	v := *u // don't mutate the server's upstream config
	u = &v
	switch u.Scheme {
	case "caldavs":
		u.Scheme = "https"
	case "caldav+insecure", "http+insecure":
		u.Scheme = "http"
	}
	if u.Scheme == "" {
		s, err := caldav.DiscoverContextURL(context.Background(), u.Host)
		if err != nil {
			srv.Logger().Printf("caldav: domain %q: failed to discover CalDAV server: %v", domain, err)
			return nil, nil
		}
		u, err = url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("caldav: Discover returned an invalid URL: %v", err)
		}
	}

	if err := sanityCheckURL(u); err != nil {
		return nil, fmt.Errorf("caldav: domain %q: failed to connect to CalDAV server %q: %v", domain, u, err)
	}

	srv.Logger().Printf("Domain %q: configured upstream CalDAV server: %v", domain, u)
	return u, nil
}

func newPlugin(srv *alps.Server) (alps.Plugin, error) {
	urls := make(map[string]*url.URL)
	for _, domain := range srv.Domains() {
		u, err := domainURL(srv, domain)
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

	p := &plugin{
		GoPlugin: alps.GoPlugin{Name: "caldav"},
		urls:     urls,
		jars:     make(map[string]http.CookieJar),
	}
	p.EnabledFunc = func(ctx *alps.Context) bool {
		if ctx.Session == nil {
			return false
		}
		_, ok := p.davURL(ctx.Session)
		return ok
	}
	p.cache = davcache.New()
	p.cache.Start()
	p.CloseFunc = func() error {
		p.cache.Stop()
		return nil
	}

	registerRoutes(p)

	return p.Plugin(), nil
}

func init() {
	alps.RegisterPluginLoader(func(s *alps.Server) ([]alps.Plugin, error) {
		p, err := newPlugin(s)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, nil
		}
		return []alps.Plugin{p}, err
	})
}
