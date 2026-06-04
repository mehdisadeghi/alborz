package alpscarddav

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"git.sr.ht/~migadu/alps"
	alpsbase "git.sr.ht/~migadu/alps/plugins/base"
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

	// Servers might require authentication to perform an OPTIONS request
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("HTTP request failed: %v %v", resp.StatusCode, resp.Status)
	}
	return nil
}

type plugin struct {
	alps.GoPlugin
	url *url.URL
}

func (p *plugin) client(session *alps.Session) (*carddav.Client, error) {
	return newClient(p.url, session)
}

func (p *plugin) clientWithAddressBooks(ctx context.Context, session *alps.Session) (*carddav.Client, []AddressBookInfo, error) {
	c, err := newClient(p.url, session)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create CardDAV client: %v", err)
	}

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query CardDAV principal: %v", err)
	}

	homeSet, err := c.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query CardDAV address book home set: %v", err)
	}

	addressBooks, err := c.FindAddressBooks(ctx, homeSet)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query CardDAV address books: %v", err)
	}
	if len(addressBooks) == 0 {
		return nil, nil, errNoAddressBook
	}

	infos := make([]AddressBookInfo, len(addressBooks))
	for i, ab := range addressBooks {
		infos[i] = AddressBookInfo{
			Path: ab.Path,
			Name: ab.Name,
		}
	}

	// Servers list collections in storage order; sort them for a stable
	// sidebar.
	sort.Slice(infos, func(i, j int) bool {
		return strings.ToLower(infos[i].Name) < strings.ToLower(infos[j].Name)
	})
	return c, infos, nil
}

func (p *plugin) clientWithAddressBook(ctx context.Context, session *alps.Session) (*carddav.Client, *AddressBookInfo, error) {
	c, addressBooks, err := p.clientWithAddressBooks(ctx, session)
	if err != nil {
		return nil, nil, err
	}
	return c, &addressBooks[0], nil
}

func newPlugin(srv *alps.Server) (alps.Plugin, error) {
	u, err := srv.Upstream("carddavs", "carddav+insecure", "https", "http+insecure")
	if _, ok := err.(*alps.NoUpstreamError); ok {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("carddav: failed to parse upstream CardDAV server: %v", err)
	}
	switch u.Scheme {
	case "carddavs":
		u.Scheme = "https"
	case "carddav+insecure", "http+insecure":
		u.Scheme = "http"
	}
	if u.Scheme == "" {
		s, err := carddav.DiscoverContextURL(context.Background(), u.Host)
		if err != nil {
			srv.Logger().Printf("carddav: failed to discover CardDAV server: %v", err)
			return nil, nil
		}
		u, err = url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("carddav: Discover returned an invalid URL: %v", err)
		}
	}

	if err := sanityCheckURL(u); err != nil {
		return nil, fmt.Errorf("carddav: failed to connect to CardDAV server %q: %v", u, err)
	}

	srv.Logger().Printf("Configured upstream CardDAV server: %v", u)

	p := &plugin{
		GoPlugin: alps.GoPlugin{Name: "carddav"},
		url:      u,
	}

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
		// TODO: cache the results
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
