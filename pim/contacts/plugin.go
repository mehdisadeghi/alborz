package contacts

import (
	"context"
	"fmt"
	"net/http"
	netmail "net/mail"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

type plugin struct {
	alborz.Router
	dav *dav.Provider
}

func (p *plugin) client(ctx context.Context, login *alborz.Login) (*carddav.Client, error) {
	return newClient(p.dav.URL(ctx, login), p.dav.HTTPClient(ctx, login))
}

func findHome(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	c, err := carddav.NewClient(client, endpoint)
	if err != nil {
		return "", err
	}
	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to query CardDAV principal: %w", err)
	}
	homeSet, err := c.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		return "", fmt.Errorf("failed to query CardDAV address book home set: %w", err)
	}
	return homeSet, nil
}

func (p *plugin) clientWithAddressBooks(ctx context.Context, login *alborz.Login) (*carddav.Client, []dav.Collection, error) {
	c, infos, err := dav.Opened(ctx, p.dav, login, p.client)
	if err != nil {
		return nil, nil, err
	}
	if len(infos) == 0 {
		return nil, nil, errNoAddressBook
	}
	return c, infos, nil
}

func (p *plugin) pooledBooks(ctx *alborz.Context) ([]dav.Account[*carddav.Client], error) {
	return dav.Pooled(ctx, p.dav, p.clientWithAddressBooks, errNoAddressBook)
}

// Mount serves contacts.
func Mount(srv *alborz.Server, kept *store.Store) error {
	provider, err := dav.NewProvider(srv, kept, dav.Kind{
		Name: "carddav", Label: "CardDAV",
		Schemes: [2]string{"carddavs", "carddav+insecure"},
		// Address books change rarely.
		Poll:     10 * time.Minute,
		Discover: carddav.DiscoverContextURL,
		Own:      func(s dav.Services) string { return s.CardDAV },
		Holds:    store.AddressBook,
		FindHome: findHome,
		List:     listAddressBooks,
		Make:     doMkcol,
		Unnamed:  "contacts",
	})
	if err != nil {
		return err
	}

	p := &plugin{
		Router: srv.Router(),
		dav:    provider,
	}

	registerRoutes(p)
	srv.OnMessage = append(srv.OnMessage, p.collectionPage().ShowOffer(p.dav))
	// Calendar registers first, so the kept collections come last.
	srv.OnServers = append(srv.OnServers, p.dav.Cards("settings.davcontacts", bookAbilities), p.dav.HereCard())
	srv.OnAccountReady = append(srv.OnAccountReady, p.warm)

	// A card attached to a mail is filed from its own row.
	srv.OnMessage = append(srv.OnMessage, func(ctx *alborz.Context, _data alborz.RenderData) error {
		data := _data.(*mail.MessageRenderData)
		if !hasAttachment(data.Message, "text/vcard", "text/x-vcard", "text/directory") {
			return nil
		}
		groups, err := p.writableBookGroups(ctx)
		if err != nil || len(groups) == 0 {
			return nil
		}
		data.ImportBooks = groups
		return nil
	})

	srv.Suggest = append(srv.Suggest, func(ctx *alborz.Context) []string {
		c, addressBooks, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Login)
		if err != nil {
			// Suggestions are a convenience: a missing or silent address book must not block compose.
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
		// One query per book, run together so compose waits one round trip.
		results := dav.Each(ctx.Request().Context(), addressBooks, func(ctx context.Context, ab dav.Collection) ([]carddav.AddressObject, error) {
			return c.QueryAddressBook(ctx, ab.Path, &query)
		})

		// Written as a recipient is, name before address, since it is inserted verbatim.
		var emails []string
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
						entry = (&netmail.Address{Name: name, Address: email}).String()
					}
					emails = append(emails, entry)
				}
			}
		}
		return emails
	})

	return nil
}

func (p *plugin) writableBookGroups(ctx *alborz.Context) ([]dav.Group, error) {
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return nil, err
	}
	return dav.WritableGroups(accounts, func(ab dav.Collection) bool { return ab.Writable }), nil
}

func bookAbilities(found dav.Advertised) []mail.Ability {
	return []mail.Ability{
		{Label: "settings.davability3", Hint: "settings.davability3hint", Have: found.Has("3")},
		{Label: "settings.davabilityacl", Hint: "settings.davabilityaclhint", Have: found.Has("access-control")},
		{Label: "settings.davabilitycards", Hint: "settings.davabilitycardshint", Have: found.Has("addressbook")},
		{Label: "settings.davabilitymkcol", Hint: "settings.davabilitymkcolhint", Have: found.Has("extended-mkcol")},
	}
}
