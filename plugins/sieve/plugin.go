package alborzsieve

import (
	"git.mehdix.org/alborz"
)

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
			Name: "sieve",
			EnabledFunc: func(ctx *alborz.Context) bool {
				return ctx.Session != nil && ctx.Server.SieveEnabled(ctx.Session.Domain())
			},
		}
		registerRoutes(&p)
		return []alborz.Plugin{p.Plugin()}, nil
	})
}
