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
			// Sieve scripts are per-account administration, but the section
			// remains reachable from merged mail. Its route redirects to one
			// capable account before performing any operation.
			EnabledFunc: func(ctx *alborz.Context) bool {
				if ctx.Session == nil {
					return false
				}
				for _, session := range ctx.Sessions() {
					if ctx.Server.SieveEnabled(session.Domain()) {
						return true
					}
				}
				return false
			},
		}
		registerRoutes(&p)
		return []alborz.Plugin{p.Plugin()}, nil
	})
}
