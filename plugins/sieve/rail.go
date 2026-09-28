package alborzsieve

import (
	"strings"

	"git.mehdix.org/alborz"
)

// rail reads extensions from the warmed connection's greeting: no round trip.
func rail(ctx *alborz.Context) map[string][]alborz.RailRow {
	path := ctx.Request().URL.Path
	rows := map[string][]alborz.RailRow{}
	var down []string
	for _, account := range sieveAccounts(ctx) {
		login := ctx.LoginFor(account.Username)
		scoped := account.Username == ctx.URLAccount()
		q := "?account=" + alborz.QueryValue(account.Username)
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
		row := func(label, at string) alborz.RailRow {
			return alborz.RailRow{Label: ctx.T(label), Href: at + q, Active: scoped && (path == at || strings.HasPrefix(path, at+"/"))}
		}
		var pages []alborz.RailRow
		if include {
			pages = append(pages, row("filters.rules", "/filters/rules"), row("filters.forwarding", "/filters/forwarding"))
			if vacation {
				pages = append(pages, row("filters.autoreply", "/filters/autoreply"))
			}
		}
		// The scripts page is active for whatever the other pages do not claim.
		scripts := alborz.RailRow{Label: ctx.T("filters.title"), Href: "/filters" + q, Active: scoped}
		for _, p := range pages {
			if p.Active {
				scripts.Active = false
			}
		}
		rows[account.Username] = append([]alborz.RailRow{scripts}, pages...)
	}
	ctx.Unreachable(down)
	return rows
}
