package alborzsieve

import (
	"strings"

	"git.mehdix.org/alborz"
)

// rail lists each account's places in this section: its scripts, and
// the pages its server can carry. The extensions come from the greeting
// the warmed connection already holds, so this costs no round trip.
func rail(ctx *alborz.Context) map[string][]alborz.RailRow {
	path := ctx.Request().URL.Path
	rows := map[string][]alborz.RailRow{}
	for _, account := range sieveAccounts(ctx) {
		session := ctx.SessionFor(account.Username)
		scoped := account.Username == ctx.URLAccount()
		q := "?account=" + alborz.AddressParam(account.Username)
		var include bool
		err := session.DoSieve(func(c alborz.SieveClient) error {
			include = hasExtension(c, "include")
			return nil
		})
		if err != nil {
			ctx.Logger().Printf("rail %s filters: %v", account.Username, err)
		}
		row := func(label, at string) alborz.RailRow {
			return alborz.RailRow{Label: ctx.T(label), Href: at + q, Active: scoped && (path == at || strings.HasPrefix(path, at+"/"))}
		}
		var pages []alborz.RailRow
		if include {
			pages = append(pages, row("filters.forwarding", "/filters/forwarding"))
		}
		// The scripts are the section's own page: whatever the others
		// do not claim is theirs.
		scripts := alborz.RailRow{Label: ctx.T("filters.title"), Href: "/filters" + q, Active: scoped}
		for _, p := range pages {
			if p.Active {
				scripts.Active = false
			}
		}
		rows[account.Username] = append([]alborz.RailRow{scripts}, pages...)
	}
	return rows
}
