package dav

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim/store"
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
func (p *Provider) Cards(title string, abilities func(Advertised) []mail.Ability) func(*alborz.Context, alborz.RenderData) error {
	return func(ctx *alborz.Context, data alborz.RenderData) error {
		servers := data.(*mail.ServersRenderData)
		for _, src := range p.Sources(ctx.Request().Context(), ctx.Login) {
			servers.Cards = append(servers.Cards, p.card(ctx, servers, title, src, abilities))
			servers.AddPlace(src.ID, src.URL.Host)
		}
		servers.AddPlace(SourceHere, originHost(ctx))
		return nil
	}
}

func (p *Provider) card(ctx *alborz.Context, servers *mail.ServersRenderData, title string, src Source, abilities func(Advertised) []mail.Ability) mail.ServerCard {
	host := src.URL.Host
	card := mail.ServerCard{Group: mail.ServerDAV, Title: ctx.T(title), Host: host, Source: src.Origin, Record: src.Record}
	if servers.Showing == mail.ServerDAV {
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
func (p *Provider) HereCard() func(*alborz.Context, alborz.RenderData) error {
	return func(ctx *alborz.Context, data alborz.RenderData) error {
		servers := data.(*mail.ServersRenderData)
		servers.Cards = append(servers.Cards, mail.ServerCard{
			Group: mail.ServerDAV, Title: ctx.T("settings.davhere"), Source: "servers.onalborz",
			Rows: []map[string]any{
				{"label": ctx.T("settings.davhereaddress"), "value": ctx.Origin() + store.Prefix + "/"},
				{"label": ctx.T("settings.davhereuser"), "value": ctx.Login.Username()},
			},
		})
		return nil
	}
}
