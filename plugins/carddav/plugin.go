package alpscarddav

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"git.sr.ht/~migadu/alps"
	alpsbase "git.sr.ht/~migadu/alps/plugins/base"
	"git.sr.ht/~migadu/alps/plugins/davcache"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

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

type plugin struct {
	alps.GoPlugin
	urls  map[string]*url.URL // CardDAV endpoint per served mail domain
	cache *davcache.Cache
}

func (p *plugin) client(session *alps.Session) (*carddav.Client, error) {
	u, ok := p.davURL(session)
	if !ok {
		return nil, errNoAddressBook
	}
	return newClient(u, p.httpClient(session))
}

// davURL resolves the session's CardDAV endpoint, falling back to the
// unnamed provider's.
func (p *plugin) davURL(session *alps.Session) (*url.URL, bool) {
	u, ok := p.urls[session.Domain()]
	if !ok {
		u, ok = p.urls[""]
	}
	return u, ok
}

func (p *plugin) clientWithAddressBooks(ctx context.Context, session *alps.Session) (*carddav.Client, []AddressBookInfo, error) {
	c, err := p.client(session)
	if err != nil {
		return nil, nil, err
	}
	davBase, _ := p.davURL(session)

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query CardDAV principal: %v", err)
	}

	homeSet, err := c.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query CardDAV address book home set: %v", err)
	}

	infos, err := listAddressBooks(ctx, p.httpClient(session), davBase, homeSet)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query CardDAV address books: %v", err)
	}
	if len(infos) == 0 {
		return nil, nil, errNoAddressBook
	}

	return c, infos, nil
}

func (p *plugin) clientWithAddressBook(ctx context.Context, session *alps.Session) (*carddav.Client, *AddressBookInfo, error) {
	c, addressBooks, err := p.clientWithAddressBooks(ctx, session)
	if err != nil {
		return nil, nil, err
	}
	return c, &addressBooks[0], nil
}

// domainURL resolves the domain's CardDAV endpoint; nil without error means
// the domain has none.
func domainURL(srv *alps.Server, domain string) (*url.URL, error) {
	u, err := srv.Upstream(domain, "carddavs", "carddav+insecure", "https", "http+insecure")
	if _, ok := err.(*alps.NoUpstreamError); ok {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("carddav: domain %q: failed to parse upstream CardDAV server: %v", domain, err)
	}
	v := *u // don't mutate the server's upstream config
	u = &v
	switch u.Scheme {
	case "carddavs":
		u.Scheme = "https"
	case "carddav+insecure", "http+insecure":
		u.Scheme = "http"
	}
	if u.Scheme == "" {
		s, err := carddav.DiscoverContextURL(context.Background(), u.Host)
		if err != nil {
			srv.Logger().Printf("carddav: domain %q: failed to discover CardDAV server: %v", domain, err)
			return nil, nil
		}
		u, err = url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("carddav: Discover returned an invalid URL: %v", err)
		}
	}

	if err := sanityCheckURL(u); err != nil {
		return nil, fmt.Errorf("carddav: domain %q: failed to connect to CardDAV server %q: %v", domain, u, err)
	}

	srv.Logger().Printf("Domain %q: configured upstream CardDAV server: %v", domain, u)
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
		GoPlugin: alps.GoPlugin{Name: "carddav"},
		urls:     urls,
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

	p.TemplateFuncs(template.FuncMap{
		"photodatauri": func(data string) template.URL {
			if data == "" {
				return ""
			}
			// Remote photo URLs would leak the viewer's address and the
			// CSP blocks them anyway; show nothing instead.
			if strings.HasPrefix(data, "http://") || strings.HasPrefix(data, "https://") {
				return ""
			}
			if strings.HasPrefix(data, "data:") {
				return template.URL(data)
			}
			return template.URL("data:image/jpeg;base64," + data)
		},
	})

	registerRoutes(p)

	p.Inject("compose.html", func(ctx *alps.Context, _data alps.RenderData) error {
		data := _data.(*alpsbase.ComposeRenderData)

		c, addressBooks, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
		if err == errNoAddressBook {
			return nil
		} else if err != nil {
			return err
		}

		query := carddav.AddressBookQuery{
			DataRequest: carddav.AddressDataRequest{
				Props: []string{vcard.FieldFormattedName, vcard.FieldEmail},
			},
			PropFilters: []carddav.PropFilter{{
				Name: vcard.FieldEmail,
			}},
		}
		var emails []string
		for _, ab := range addressBooks {
			addrs, err := c.QueryAddressBook(ctx.Request().Context(), ab.Path, &query)
			if err != nil {
				return fmt.Errorf("failed to query CardDAV addresses: %v", err)
			}
			for _, addr := range addrs {
				emails = append(emails, addr.Card.Values(vcard.FieldEmail)...)
			}
		}

		data.Extra["EmailSuggestions"] = emails
		return nil
	})

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
