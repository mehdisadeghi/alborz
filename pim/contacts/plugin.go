package contacts

import (
	"context"
	"fmt"
	"net/http"
	netmail "net/mail"
	"slices"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/settings"
	"github.com/emersion/go-webdav/carddav"
)

type plugin struct {
	alborz.Router
	dav *dav.Provider
	// remembering keeps two writers from making the account's Previous recipients twice.
	remembering sync.Mutex
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

func (p *plugin) pooledBooks(ctx *alborz.Context) ([]pim.Account[*carddav.Client], error) {
	return pim.Pooled(ctx, p.dav, p.clientWithAddressBooks, errNoAddressBook)
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
	srv.OnAccountReady = append(srv.OnAccountReady, p.warm, p.seed)

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

	srv.OnMessage = append(srv.OnMessage, func(ctx *alborz.Context, _data alborz.RenderData) error {
		data := _data.(*mail.MessageRenderData)
		groups, err := p.writableBookGroups(ctx)
		if err != nil || len(groups) == 0 {
			return nil
		}
		var addrs []*netmail.Address
		env := data.Message.Envelope
		for _, a := range slices.Concat(env.From, env.To, env.Cc) {
			addrs = append(addrs, &netmail.Address{Name: a.Name, Address: a.Addr()})
		}
		data.Contact = &mail.ContactOffer{Addresses: distinct(addrs), Books: groups}
		return nil
	})

	srv.OnSent = append(srv.OnSent, func(ctx *alborz.Context, sender *alborz.Login, to []*netmail.Address) error {
		return p.remember(ctx.Request().Context(), sender, ctx.T("contacts.previous"), to, false)
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
		addrs, err := emails(ctx.Request().Context(), c, addressBooks)
		if err != nil {
			ctx.Logger().Printf("carddav: no suggestions from one book: %v", err)
		}
		// Written as a recipient is, name before address, since it is inserted verbatim.
		entries := make([]string, len(addrs))
		for i, a := range addrs {
			entries[i] = a.Address
			if a.Name != "" {
				entries[i] = a.String()
			}
		}
		return entries
	})

	return nil
}

func (p *plugin) writableBookGroups(ctx *alborz.Context) ([]dav.Group, error) {
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return nil, err
	}
	return pim.WritableGroups(accounts, func(ab dav.Collection) bool { return ab.Writable }), nil
}

func bookAbilities(found dav.Advertised) []settings.Ability {
	return []settings.Ability{
		{Label: "settings.davability3", Hint: "settings.davability3hint", Have: found.Has("3")},
		{Label: "settings.davabilityacl", Hint: "settings.davabilityaclhint", Have: found.Has("access-control")},
		{Label: "settings.davabilitycards", Hint: "settings.davabilitycardshint", Have: found.Has("addressbook")},
		{Label: "settings.davabilitymkcol", Hint: "settings.davabilitymkcolhint", Have: found.Has("extended-mkcol")},
	}
}
