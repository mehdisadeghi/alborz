package dav

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/web"
)

// placeHost is the host a reader knows a source by; alborz knows its own from -public-url, else from the request.
func placeHost(ctx *web.Context, src Source) string {
	if src.ID != SourceHere {
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
func (p *Provider) Places(ctx *web.Context) []Group {
	var groups []Group
	for _, s := range ctx.Logins() {
		var places []Collection
		for _, src := range p.Sources(ctx.Request().Context(), s) {
			places = append(places, Collection{Path: src.ID, Name: PlaceLabel(ctx, src.ID, placeHost(ctx, src))})
		}
		groups = append(groups, Group{Account: s.Username(), Collections: places})
	}
	return groups
}

// ReadPlace is the form's chosen place, else the account's reachable default, its first source, or Alborz.
func (p *Provider) ReadPlace(ctx *web.Context, account string) (string, string) {
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
		return account, SourceHere
	}
	return account, p.DefaultPlace(ctx.Request().Context(), s)
}

// HandleRefresh asks every signed-in account's server again and lands on list, a route.
func (p *Provider) HandleRefresh(list string) func(*web.Context) error {
	return func(ctx *web.Context) error {
		p.Refresh(ctx.Request().Context(), ctx.Logins())
		// The list itself is what was asked for.
		ctx.Quiet()
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.URL(list)))
	}
}

// Guarded sends an account with none of the kind to create its first, at the route create, instead of failing.
func (p *Provider) Guarded(none error, create string, h func(*web.Context) error) func(*web.Context) error {
	return func(ctx *web.Context) error {
		if err := h(ctx); !errors.Is(err, none) {
			return err
		}
		return ctx.Redirect(http.StatusFound, ctx.URL(create))
	}
}

// Cards put each of the kind's sources among the account's servers.
func (p *Provider) Cards(title string, abilities func(Advertised) []web.Ability) func(*web.Context, *web.Servers) error {
	return func(ctx *web.Context, servers *web.Servers) error {
		for _, src := range p.Sources(ctx.Request().Context(), ctx.Login) {
			servers.AddPlace(src.ID, placeHost(ctx, src))
			// This alborz tells other clients how to reach it instead; HereCard is its card.
			if src.ID != SourceHere {
				servers.Cards = append(servers.Cards, p.card(ctx, servers, title, src, abilities))
			}
		}
		return nil
	}
}

func (p *Provider) card(ctx *web.Context, servers *web.Servers, title string, src Source, abilities func(Advertised) []web.Ability) web.ServerCard {
	host := src.URL.Host
	card := web.ServerCard{Group: web.ServerDAV, Title: ctx.T(title), Host: host, Source: src.Origin, Record: src.Record}
	if servers.Showing == web.ServerDAV {
		endpoint, _ := url.Parse(src.Endpoint())
		found, err := Describe(ctx.Request().Context(), p.HTTPClient(ctx.Request().Context(), ctx.Login), endpoint)
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
func (p *Provider) HereCard() func(*web.Context, *web.Servers) error {
	return func(ctx *web.Context, servers *web.Servers) error {
		servers.Cards = append(servers.Cards, web.ServerCard{
			Group: web.ServerDAV, Title: ctx.T("settings.davhere"), Source: "servers.onalborz",
			Rows: []map[string]any{
				{"label": ctx.T("settings.davhereaddress"), "value": ctx.Origin() + store.Prefix + "/"},
				{"label": ctx.T("settings.davhereuser"), "value": ctx.Login.Username()},
			},
		})
		return nil
	}
}

// RailInvitations are a section's open invitations and the route of the collection page their answers post under.
type RailInvitations struct {
	Collection string
	Items      []Invitation
}

// RailInvitations are the open invitations the section's rail offers, answered under the collection route; nil for none.
func (p *Provider) RailInvitations(ctx *web.Context, collection string) (*RailInvitations, error) {
	items, err := p.Invitations(ctx.Logins())
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &RailInvitations{Collection: collection, Items: items}, nil
}
