package dav

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/collections"
)

// Advertised is what a DAV server says about itself when asked, and
// nothing alborz worked out for itself. A reader whose calendar behaves
// oddly is looking at somebody else's deployment, and the first thing
// worth knowing is which one and what it claims to do.
type Advertised struct {
	// Software is the Server header, which is the server's own claim
	// and often absent.
	Software string
	// Compliance are the classes of the DAV header, in the order the
	// server listed them: "1", "2", "3", "access-control",
	// "calendar-access", "addressbook" and whatever else it supports
	// (RFC 4918 10.1).
	Compliance []string
	// Principal is the current user's principal URL and Home the
	// collection home this account's calendars or address books hang
	// off (RFC 5397, RFC 4791 6.2.1, RFC 6352 7.1.1).
	Principal string
	Home      string
}

// Has reports whether the server listed a compliance class.
func (a Advertised) Has(class string) bool {
	return slices.ContainsFunc(a.Compliance, func(c string) bool {
		return strings.EqualFold(c, class)
	})
}

// Describe asks the server what it is. One OPTIONS, which every DAV
// server answers before anything else happens on a connection anyway;
// the principal and the home come from the caller, which has already
// found them to list the collections.
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

// InjectCard puts each of the kind's sources among the account's
// servers. The list needs only where each is; the server's own page
// asks each what it claims, which is one OPTIONS every DAV server
// answers anyway.
func (p *Provider) InjectCard(title string, abilities func(Advertised) []alborzbase.Ability) alborz.InjectFunc {
	return func(ctx *alborz.Context, data alborz.RenderData) error {
		servers, ok := data.(*alborzbase.ServersRenderData)
		if !ok || ctx.Session == nil {
			return nil
		}
		for _, src := range p.Sources(ctx.Session) {
			servers.Cards = append(servers.Cards, p.card(ctx, servers, title, src, abilities))
			servers.AddPlace(src.ID, src.URL.Host)
		}
		if p.here != nil {
			servers.AddPlace(SourceHere, originHost(ctx))
		}
		return nil
	}
}

func (p *Provider) card(ctx *alborz.Context, servers *alborzbase.ServersRenderData, title string, src Source, abilities func(Advertised) []alborzbase.Ability) alborzbase.ServerCard {
	host := src.URL.Host
	card := alborzbase.ServerCard{Group: alborzbase.ServerDAV, Title: ctx.T(title), Host: host, Source: src.Origin, Record: src.Record}
	if servers.Showing == alborzbase.ServerDAV {
		endpoint, _ := url.Parse(src.Endpoint())
		found, err := Describe(ctx.Request().Context(), p.HTTPClient(ctx.Session), endpoint)
		// A server that did not answer is worth saying so about; the
		// page is not the place to fail over it.
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

// InjectHere puts the collections kept here among the account's
// servers, with what a phone is given to sync them.
func (p *Provider) InjectHere() alborz.InjectFunc {
	return func(ctx *alborz.Context, data alborz.RenderData) error {
		servers, ok := data.(*alborzbase.ServersRenderData)
		if !ok || ctx.Session == nil || p.here == nil {
			return nil
		}
		servers.Cards = append(servers.Cards, alborzbase.ServerCard{
			Group: alborzbase.ServerDAV, Title: ctx.T("settings.davhere"), Source: "servers.onalborz",
			Rows: []map[string]any{
				{"label": ctx.T("settings.davhereaddress"), "value": ctx.Origin() + collections.Prefix + "/"},
				{"label": ctx.T("settings.davhereuser"), "value": ctx.Session.Username()},
			},
		})
		return nil
	}
}
