package alborzsieve

import (
	"embed"

	"git.mehdix.org/alborz"
)

//go:embed all:public
var public embed.FS

func init() {
	alborz.RegisterPluginLoader(func(srv *alborz.Server) ([]alborz.Plugin, error) {
		enabled := false
		for _, domain := range srv.Domains() {
			if srv.SieveEnabled(domain) {
				enabled = true
			}
		}
		if !enabled {
			return nil, nil
		}

		p := alborz.GoPlugin{
			Name:  "sieve",
			Files: public,
			// Sieve scripts are per-account administration; the unified
			// view has no single account to administer.
			EnabledFunc: func(ctx *alborz.Context) bool {
				return ctx.Session != nil && !ctx.Unified &&
					ctx.Server.SieveEnabled(ctx.Session.Domain())
			},
		}
		registerRoutes(&p)
		return []alborz.Plugin{p.Plugin()}, nil
	})
}
