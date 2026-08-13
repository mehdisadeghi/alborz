package alpssieve

import (
	"git.sr.ht/~migadu/alps"
)

func init() {
	alps.RegisterPluginLoader(func(srv *alps.Server) ([]alps.Plugin, error) {
		enabled := false
		for _, domain := range srv.Domains() {
			if srv.SieveEnabled(domain) {
				enabled = true
			}
		}
		if !enabled {
			return nil, nil
		}

		p := alps.GoPlugin{
			Name: "sieve",
			EnabledFunc: func(ctx *alps.Context) bool {
				return ctx.Session != nil && ctx.Server.SieveEnabled(ctx.Session.Domain())
			},
		}
		registerRoutes(&p)
		return []alps.Plugin{p.Plugin()}, nil
	})
}
