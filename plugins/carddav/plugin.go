package alborzcarddav

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

//go:embed all:public
var public embed.FS

type plugin struct {
	alborz.GoPlugin
	dav *dav.Provider
}

func (p *plugin) client(session *alborz.Session) (*carddav.Client, error) {
	u, ok := p.dav.URL(session)
	if !ok {
		return nil, errNoAddressBook
	}
	return newClient(u, p.dav.HTTPClient(session))
}

// findHome is where the account's server lists the account's address books.
func findHome(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	c, err := carddav.NewClient(client, endpoint)
	if err != nil {
		return "", err
	}
	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to query CardDAV principal: %v", err)
	}
	homeSet, err := c.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		return "", fmt.Errorf("failed to query CardDAV address book home set: %v", err)
	}
	return homeSet, nil
}

// An account with no address book has no contacts section to show, and
// says so rather than listing nothing.
func (p *plugin) clientWithAddressBooks(ctx context.Context, session *alborz.Session) (*carddav.Client, []dav.Collection, error) {
	c, infos, err := dav.Opened(ctx, p.dav, session, p.client)
	if err != nil {
		return nil, nil, err
	}
	if len(infos) == 0 {
		return nil, nil, errNoAddressBook
	}
	return c, infos, nil
}

// pooledBooks resolves every signed-in account that has CardDAV: the
// contacts page is always pooled across accounts.
func (p *plugin) pooledBooks(ctx *alborz.Context) ([]dav.Account[*carddav.Client], error) {
	return dav.Pooled(ctx, p.dav, p.clientWithAddressBooks, errNoAddressBook)
}

func newPlugin(srv *alborz.Server) (alborz.Plugin, error) {
	provider, err := dav.NewProvider(srv, dav.Kind{
		Name: "carddav", Label: "CardDAV",
		Schemes: [2]string{"carddavs", "carddav+insecure"},
		// Address books change rarely; asking every few minutes is
		// asking for nothing.
		Poll:     10 * time.Minute,
		Discover: carddav.DiscoverContextURL,
		FindHome: findHome,
		List:     listAddressBooks,
		Make:     doMkcol,
		Unnamed:  "contacts",
	})
	if err != nil || provider == nil {
		return nil, err
	}

	p := &plugin{
		GoPlugin: alborz.GoPlugin{Name: "carddav", Files: public},
		dav:      provider,
	}
	p.EnabledFunc = provider.Enabled
	p.CloseFunc = provider.Close

	registerRoutes(p)
	srv.OnAccountReady = append(srv.OnAccountReady, p.warm)

	// A card attached to a mail is filed from its own row, into a book
	// the reader chooses.
	p.Inject("message.html", func(ctx *alborz.Context, _data alborz.RenderData) error {
		data := _data.(*alborzbase.MessageRenderData)
		if !hasAttachment(data.Message, "text/vcard", "text/x-vcard", "text/directory") {
			return nil
		}
		groups, err := p.writableBookGroups(ctx)
		if err != nil || len(groups) == 0 {
			return nil
		}
		if data.Extra == nil {
			data.Extra = make(map[string]interface{})
		}
		data.Extra["ImportBooks"] = groups
		return nil
	})

	p.Inject("compose.html", func(ctx *alborz.Context, _data alborz.RenderData) error {
		data := _data.(*alborzbase.ComposeRenderData)

		c, addressBooks, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
		if err != nil {
			// Suggestions are a convenience of the compose page, not a
			// condition of it: an address book that is missing or not
			// answering must not keep a message from being written.
			if err != errNoAddressBook {
				ctx.Logger().Printf("carddav: no suggestions for compose: %v", err)
			}
			return nil
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
		results := dav.Each(ctx.Request().Context(), addressBooks, func(ctx context.Context, ab dav.Collection) ([]carddav.AddressObject, error) {
			return c.QueryAddressBook(ctx, ab.Path, &query)
		})

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
			if result.Err != nil {
				ctx.Logger().Printf("carddav: no suggestions from one book: %v", result.Err)
				continue
			}
			for _, addr := range result.Value {
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

func (p *plugin) writableBookGroups(ctx *alborz.Context) ([]dav.Group, error) {
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return nil, err
	}
	return dav.WritableGroups(accounts, func(ab dav.Collection) bool { return ab.Writable }), nil
}
