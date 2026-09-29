package dav

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git.mehdix.org/alborz/pim/store"
	"git.mehdix.org/alborz/settings"
	"git.mehdix.org/alborz/web"
)

// Advertised is what a DAV server claims about itself, nothing alborz inferred.
type Advertised struct {
	// Software is the Server header, often absent.
	Software string
	// Compliance are the DAV header's classes in server order (RFC 4918 10.1).
	Compliance []string
	// Principal is the current user's principal URL, Home the collection home (RFC 5397, RFC 4791 6.2.1, RFC 6352 7.1.1).
	Principal string
	Home      string
}

// Has reports whether the server listed a compliance class.
func (a Advertised) Has(class string) bool {
	return slices.ContainsFunc(a.Compliance, func(c string) bool {
		return strings.EqualFold(c, class)
	})
}

// Describe asks the server what it is with one OPTIONS request.
func Describe(ctx context.Context, client *http.Client, base *url.URL) (Advertised, error) {
	var a Advertised
	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, base.String(), nil)
	if err != nil {
		return a, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return a, err
	}
	defer resp.Body.Close()

	a.Software = resp.Header.Get("Server")
	for _, class := range strings.Split(resp.Header.Get("DAV"), ",") {
		if class = strings.TrimSpace(class); class != "" {
			a.Compliance = append(a.Compliance, class)
		}
	}
	return a, nil
}

// Cards put each of the kind's sources among the account's servers.
func (p *Provider) Cards(title string, abilities func(Advertised) []settings.Ability) func(*web.Context, web.RenderData) error {
	return func(ctx *web.Context, data web.RenderData) error {
		servers := data.(*settings.ServersRenderData)
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

func (p *Provider) card(ctx *web.Context, servers *settings.ServersRenderData, title string, src Source, abilities func(Advertised) []settings.Ability) settings.ServerCard {
	host := src.URL.Host
	card := settings.ServerCard{Group: settings.ServerDAV, Title: ctx.T(title), Host: host, Source: src.Origin, Record: src.Record}
	if servers.Showing == settings.ServerDAV {
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
func (p *Provider) HereCard() func(*web.Context, web.RenderData) error {
	return func(ctx *web.Context, data web.RenderData) error {
		servers := data.(*settings.ServersRenderData)
		servers.Cards = append(servers.Cards, settings.ServerCard{
			Group: settings.ServerDAV, Title: ctx.T("settings.davhere"), Source: "servers.onalborz",
			Rows: []map[string]any{
				{"label": ctx.T("settings.davhereaddress"), "value": ctx.Origin() + store.Prefix + "/"},
				{"label": ctx.T("settings.davhereuser"), "value": ctx.Login.Username()},
			},
		})
		return nil
	}
}
