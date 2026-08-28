package alborzcarddav

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/davcache"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

//go:embed all:public
var public embed.FS

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

// Age at which a discovered address book list is reloaded in the background
// while still being served; alborz cannot create or delete books, so a
// change made on the server shows up one visit late at worst.
const discoveryTTL = 5 * time.Minute

// The discovery load is detached from its requester's context, so a
// deadline of its own is all that keeps a hung server from wedging every
// waiter behind the memo.
const discoveryTimeout = 30 * time.Second

const requestTimeout = 10 * time.Second

type plugin struct {
	alborz.GoPlugin
	urls  map[string]*url.URL // CardDAV endpoint per served mail domain
	cache *davcache.Cache

	// Discovered address book list per username; see clientWithAddressBooks.
	books *alborz.Memo[[]AddressBookInfo]

	jarsMu sync.Mutex
	jars   map[string]http.CookieJar // per username
}

func (p *plugin) client(session *alborz.Session) (*carddav.Client, error) {
	u, ok := p.davURL(session)
	if !ok {
		return nil, errNoAddressBook
	}
	return newClient(u, p.httpClient(session))
}

// jar returns the user's persistent cookie jar. Reusing the DAV server's
// session cookie lets it skip re-authenticating every single request.
func (p *plugin) jar(session *alborz.Session) http.CookieJar {
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

// davURL resolves the session's CardDAV endpoint, falling back to the
// unnamed provider's.
func (p *plugin) davURL(session *alborz.Session) (*url.URL, bool) {
	u, ok := p.urls[session.Domain()]
	if !ok {
		u, ok = p.urls[""]
	}
	return u, ok
}

func (p *plugin) clientWithAddressBooks(ctx context.Context, session *alborz.Session) (*carddav.Client, []AddressBookInfo, error) {
	c, err := p.client(session)
	if err != nil {
		return nil, nil, err
	}
	davBase, _ := p.davURL(session)

	// Principal, home set, and book list are three sequential round trips
	// answering which address books the account has, so they are found once
	// per user rather than on every page, compose included. The load
	// outlives the request that starts it: a second page waiting on it must
	// not be failed by the first one's reader going away.
	infos, err := p.books.Get(session.Username(), func() ([]AddressBookInfo, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryTimeout)
		defer cancel()

		principal, err := c.FindCurrentUserPrincipal(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to query CardDAV principal: %v", err)
		}

		homeSet, err := c.FindAddressBookHomeSet(ctx, principal)
		if err != nil {
			return nil, fmt.Errorf("failed to query CardDAV address book home set: %v", err)
		}

		infos, err := listAddressBooks(ctx, p.httpClient(session), davBase, homeSet)
		if err != nil {
			return nil, fmt.Errorf("failed to query CardDAV address books: %v", err)
		}
		return infos, nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(infos) == 0 {
		return nil, nil, errNoAddressBook
	}

	return c, infos, nil
}

func (p *plugin) clientWithAddressBook(ctx context.Context, session *alborz.Session) (*carddav.Client, *AddressBookInfo, error) {
	c, addressBooks, err := p.clientWithAddressBooks(ctx, session)
	if err != nil {
		return nil, nil, err
	}
	return c, &addressBooks[0], nil
}

// domainURL resolves the domain's CardDAV endpoint; nil without error means
// the domain has none.
func domainURL(srv *alborz.Server, domain string) (*url.URL, error) {
	u, err := srv.Upstream(domain, "carddavs", "carddav+insecure", "https", "http+insecure")
	if _, ok := err.(*alborz.NoUpstreamError); ok {
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

	// An unreachable DAV server must not keep the whole webmail from
	// starting; requests surface the failure until it recovers.
	if err := sanityCheckURL(u); err != nil {
		srv.Logger().Printf("Warning: carddav: domain %q: CardDAV server %q not reachable at startup: %v", domain, u, err)
	}

	srv.Logger().Printf("Domain %q: configured upstream CardDAV server: %v", domain, u)
	return u, nil
}

func newPlugin(srv *alborz.Server) (alborz.Plugin, error) {
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
		GoPlugin: alborz.GoPlugin{Name: "carddav", Files: public},
		urls:     urls,
		books:    alborz.NewBackgroundMemo[[]AddressBookInfo](discoveryTTL),
		jars:     make(map[string]http.CookieJar),
	}
	p.EnabledFunc = func(ctx *alborz.Context) bool {
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

	p.Inject("compose.html", func(ctx *alborz.Context, _data alborz.RenderData) error {
		data := _data.(*alborzbase.ComposeRenderData)

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
		// One query per book, run together like the contacts page does;
		// sequentially they would delay the compose form by a round trip
		// each.
		type bookResult struct {
			addrs []carddav.AddressObject
			err   error
		}
		reqCtx := ctx.Request().Context()
		results := make([]bookResult, len(addressBooks))
		var wg sync.WaitGroup
		sem := make(chan struct{}, maxAddressBookQueryConcurrency)
		for i, ab := range addressBooks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				results[i].addrs, results[i].err = c.QueryAddressBook(reqCtx, ab.Path, &query)
			}()
		}
		wg.Wait()

		var emails []string
		for _, result := range results {
			if result.err != nil {
				return fmt.Errorf("failed to query CardDAV addresses: %v", result.err)
			}
			for _, addr := range result.addrs {
				emails = append(emails, addr.Card.Values(vcard.FieldEmail)...)
			}
		}

		data.Extra["EmailSuggestions"] = emails
		return nil
	})

	return p.Plugin(), nil
}

func init() {
	alborz.RegisterPluginLoader(func(s *alborz.Server) ([]alborz.Plugin, error) {
		p, err := newPlugin(s)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, nil
		}
		return []alborz.Plugin{p}, err
	})
}
