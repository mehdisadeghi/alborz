package filters

import (
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

// rail reads extensions from the warmed connection's greeting: no round trip.
func rail(ctx *web.Context) map[string][]web.RailRow {
	path := ctx.Request().URL.Path
	rows := map[string][]web.RailRow{}
	var down []string
	for _, account := range sieveAccounts(ctx) {
		login := ctx.LoginFor(account.Username)
		scoped := account.Username == ctx.URLAccount()
		q := "?account=" + web.QueryValue(account.Username)
		var include, vacation bool
		err := login.DoSieve(func(c alborz.SieveClient) error {
			include = hasExtension(c, "include")
			vacation = hasExtension(c, "vacation")
			return nil
		})
		if err != nil {
			down = append(down, account.Username)
			ctx.Logger().Printf("rail %s filters: %v", account.Username, err)
		}
		row := func(label, at string) web.RailRow {
			return web.RailRow{Label: ctx.T(label), Href: at + q, Active: scoped && (path == at || strings.HasPrefix(path, at+"/"))}
		}
		var pages []web.RailRow
		if include {
			pages = append(pages, row("filters.rules", ctx.Path("filters.rules")), row("filters.forwarding", ctx.Path("filters.forwarding")))
			if vacation {
				pages = append(pages, row("filters.autoreply", ctx.Path("filters.autoreply")))
			}
		}
		// The scripts page is active for whatever the other pages do not claim.
		scripts := web.RailRow{Label: ctx.T("filters.title"), Href: ctx.Path("filters.list") + q, Active: scoped}
		for _, p := range pages {
			if p.Active {
				scripts.Active = false
			}
		}
		rows[account.Username] = append([]web.RailRow{scripts}, pages...)
	}
	ctx.Unreachable(down)
	return rows
}
