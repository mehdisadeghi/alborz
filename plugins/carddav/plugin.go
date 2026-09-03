package alborzcarddav

import (
	"context"
	"embed"
	"fmt"
	"github.com/labstack/echo/v4"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strings"
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

	client := &http.Client{Timeout: alborz.RoundTripTimeout}
	resp, err := client.Do(req)
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

	// Set in debug mode; logs upstream DAV traffic.
	debug echo.Logger
}

func (p *plugin) client(session *alborz.Session) (*carddav.Client, error) {
	u, ok := p.davURL(session)
	if !ok {
		return nil, errNoAddressBook
	}
	return newClient(u, p.httpClient(session))
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

// accountBooks is one signed-in account's address book set.
type accountBooks struct {
	account string
	session *alborz.Session
	client  *carddav.Client
	books   []AddressBookInfo
}

// pooledBooks resolves every signed-in account that has CardDAV: the
// contacts page is always pooled across accounts. An account that fails
// is logged and skipped; the error surfaces only when no account
// answered.
func (p *plugin) pooledBooks(ctx *alborz.Context) ([]accountBooks, error) {
	var accounts []accountBooks
	var lastErr error
	for _, s := range ctx.Sessions() {
		if _, ok := p.davURL(s); !ok {
			continue
		}
		c, books, err := p.clientWithAddressBooks(ctx.Request().Context(), s)
		if err != nil {
			lastErr = err
			ctx.Logger().Printf("carddav: skipping %q in the pooled view: %v", s.Username(), err)
			continue
		}
		infos := make([]AddressBookInfo, len(books))
		for i, ab := range books {
			infos[i] = ab
			infos[i].Account = s.Username()
		}
		accounts = append(accounts, accountBooks{account: s.Username(), session: s, client: c, books: infos})
	}
	if len(accounts) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errNoAddressBook
	}
	return accounts, nil
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
		// Startup must not hang on a DAV host that swallows packets.
		ctx, cancel := context.WithTimeout(context.Background(), alborz.RoundTripTimeout)
		defer cancel()
		s, err := carddav.DiscoverContextURL(ctx, u.Host)
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
	}
	p.EnabledFunc = func(ctx *alborz.Context) bool {
		if ctx.Session == nil {
			return false
		}
		// Pooled pages exist when any account has the service.
		for _, s := range ctx.Sessions() {
			if _, ok := p.davURL(s); ok {
				return true
			}
		}
		return false
	}
	if srv.Options.Debug {
		p.debug = srv.Logger()
	}
	p.cache = davcache.New()
	p.cache.Start()
	// Signing out is the only thing that ends the cache's authority to
	// hold this account's collections; idleness no longer does.
	cache := p.cache
	srv.OnAccountGone = append(srv.OnAccountGone, cache.Forget)
	p.CloseFunc = func() error {
		cache.Stop()
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

		// A suggestion is inserted verbatim into the field, so it is
		// written the way a recipient is written (RFC 5322 3.4): the name
		// in front of the address. A bare address makes the reader type
		// the name back in, and a card with several addresses would
		// otherwise offer them with nothing to tell them apart.
		// The base plugin has already put the people this account
		// exchanges mail with here; the address books add the ones it
		// was told to keep. Replacing rather than adding would drop the
		// larger half.
		var emails []string
		seen := make(map[string]bool)
		if existing, ok := data.Extra["EmailSuggestions"].([]string); ok {
			for _, entry := range existing {
				if key := strings.ToLower(entry); !seen[key] {
					seen[key] = true
					emails = append(emails, entry)
				}
			}
		}
		for _, result := range results {
			if result.err != nil {
				return fmt.Errorf("failed to query CardDAV addresses: %v", result.err)
			}
			for _, addr := range result.addrs {
				name := addr.Card.Value(vcard.FieldFormattedName)
				for _, email := range addr.Card.Values(vcard.FieldEmail) {
					if email == "" {
						continue
					}
					entry := email
					if name != "" {
						entry = (&mail.Address{Name: name, Address: email}).String()
					}
					if key := strings.ToLower(entry); !seen[key] {
						seen[key] = true
						emails = append(emails, entry)
					}
				}
			}
		}
		slices.Sort(emails)

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

// BookGroup is one account's writable address books, for create forms.
type BookGroup struct {
	Account     string
	Collections []AddressBookInfo
}

// writableBookGroups lists every account's writable books, so a create
// form can offer any account as the destination.
func (p *plugin) writableBookGroups(ctx *alborz.Context) ([]BookGroup, error) {
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return nil, err
	}
	var groups []BookGroup
	for _, acc := range accounts {
		var books []AddressBookInfo
		for _, ab := range acc.books {
			if ab.Writable {
				books = append(books, ab)
			}
		}
		if len(books) > 0 {
			groups = append(groups, BookGroup{Account: acc.account, Collections: books})
		}
	}
	return groups, nil
}
