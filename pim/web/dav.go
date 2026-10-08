package web

import (
	"errors"
	"net/url"
	"strings"

	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
)

// placeHost is the host a reader knows a source by; alborz knows its own from -public-url, else from the request.
func placeHost(ctx *web.Context, src dav.Source) string {
	if src.ID != dav.SourceHere {
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

// Places are where a new collection can go, grouped by account.
func Places(ctx *web.Context, p *dav.Provider) []dav.Group {
	var groups []dav.Group
	for _, s := range ctx.Logins() {
		var places []dav.Collection
		for _, src := range p.Sources(ctx.Request().Context(), s) {
			places = append(places, dav.Collection{Path: src.ID, Name: PlaceLabel(ctx, src.ID, placeHost(ctx, src))})
		}
		groups = append(groups, dav.Group{Account: s.Username(), Collections: places})
	}
	return groups
}

// ReadPlace is the form's chosen place, else the account's reachable default, its first source, or Alborz.
func ReadPlace(ctx *web.Context, p *dav.Provider, account string) (string, string) {
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
		return account, dav.SourceHere
	}
	return account, p.DefaultPlace(ctx.Request().Context(), s)
}

// HandleRefresh asks every signed-in account's server again and lands on list, a route.
func HandleRefresh(p *dav.Provider, list string) func(*web.Context) error {
	return func(ctx *web.Context) error {
		p.Refresh(ctx.Request().Context(), ctx.Logins())
		// The list itself is what was asked for.
		return ctx.QuietRedirect(ctx.NextOr(ctx.URL(list)))
	}
}

// Guarded sends an account with none of the kind to create its first, at the route create, instead of failing.
func Guarded(none error, create string, h func(*web.Context) error) func(*web.Context) error {
	return func(ctx *web.Context) error {
		if err := h(ctx); !errors.Is(err, none) {
			return err
		}
		return ctx.FoundRedirect(ctx.URL(create))
	}
}

// GuardedRoutes applies one missing-resource guard to the common page routes.
func GuardedRoutes(g *web.Group, guard func(func(*web.Context) error) func(*web.Context) error) (get, post, page, download func(string, string, func(*web.Context) error)) {
	route := func(add func(string, string, web.HandlerFunc)) func(string, string, func(*web.Context) error) {
		return func(name, path string, h func(*web.Context) error) { add(name, path, guard(h)) }
	}
	get, post, download = route(g.GET), route(g.POST), route(g.Download)
	page = func(name, path string, h func(*web.Context) error) { get(name, path, h); post(name, path, h) }
	return
}

// GuardedUpload registers both outcomes of a bounded upload through the same guard.
func GuardedUpload(g *web.Group, guard func(func(*web.Context) error) func(*web.Context) error) func(string, string, int64, func(*web.Context) error, func(*web.Context) error) {
	return func(name, path string, limit int64, h, over func(*web.Context) error) {
		g.Upload(name, path, limit, guard(h), guard(over))
	}
}

// Cards put each of the kind's sources among the account's servers.
func Cards(p *dav.Provider, title string, abilities func(dav.Advertised) []web.Ability) func(*web.Context, *web.Servers) error {
	return func(ctx *web.Context, servers *web.Servers) error {
		for _, src := range p.Sources(ctx.Request().Context(), ctx.Login) {
			servers.AddPlace(src.ID, placeHost(ctx, src))
			// This alborz tells other clients how to reach it instead; HereCard is its card.
			if src.ID != dav.SourceHere {
				servers.Cards = append(servers.Cards, card(ctx, p, servers, title, src, abilities))
			}
		}
		return nil
	}
}

func card(ctx *web.Context, p *dav.Provider, servers *web.Servers, title string, src dav.Source, abilities func(dav.Advertised) []web.Ability) web.ServerCard {
	host := src.URL.Host
	card := web.ServerCard{Group: web.ServerDAV, Title: ctx.T(title), Host: host, Source: src.Origin, Record: src.Record}
	if servers.Showing == web.ServerDAV {
		endpoint, _ := url.Parse(src.Endpoint())
		found, err := dav.Describe(ctx.Request().Context(), p.HTTPClient(ctx.Request().Context(), ctx.Login), endpoint)
		// An unreachable server is said on the page, not failed over.
		if err != nil {
			card.Rows = []map[string]any{
				{"label": ctx.T("settings.serverhost"), "value": host},
				{"label": ctx.T("settings.serverunreachable"), "value": err.Error()},
			}
		} else {
			card.Rows = []map[string]any{
				{"label": ctx.T("settings.serverhost"), "value": host},
				{"label": ctx.T("settings.serversource"), "value": card.SourceText(ctx.T)},
				{"label": ctx.T("settings.serversoftware"), "value": found.Software},
				{"label": ctx.T("settings.davcompliance"), "value": strings.Join(found.Compliance, ", ")},
			}
			card.Abilities = abilities(found)
		}
	}
	return card
}

// HereCard puts the collections kept here among the account's servers.
func HereCard(ctx *web.Context, servers *web.Servers) error {
	servers.Cards = append(servers.Cards, web.ServerCard{
		Group: web.ServerDAV, Title: ctx.T("settings.davhere"), Source: "servers.onalborz",
		Rows: []map[string]any{
			{"label": ctx.T("settings.davhereaddress"), "value": ctx.Origin() + ctx.Path("dav.collections")},
			{"label": ctx.T("settings.davhereuser"), "value": ctx.Login.Username()},
		},
	})
	return nil
}

// RailInvitations are a section's open invitations and the route of the collection page their answers post under.
type RailInvitations struct {
	Collection string
	Items      []dav.Invitation
}

// Invitations are the open invitations the section's rail offers, answered under the collection route; nil for none.
func Invitations(ctx *web.Context, p *dav.Provider, collection string) (*RailInvitations, error) {
	items, err := p.Invitations(ctx.Logins())
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &RailInvitations{Collection: collection, Items: items}, nil
}
