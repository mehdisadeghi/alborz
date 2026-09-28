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
			// Filters stay reachable from merged mail; the route redirects to a capable account.
			EnabledFunc: func(ctx *alborz.Context) bool {
				if ctx.Login == nil {
					return false
				}
				for _, login := range ctx.Logins() {
					if ctx.Server.SieveEnabled(login.Domain()) {
						return true
					}
				}
				return false
			},
		}
		registerRoutes(&p)
		srv.OnAccountReady = append(srv.OnAccountReady, warm)
		return []alborz.Plugin{p.Plugin()}, nil
	})
}

// warm dials ManageSieve at sign-in so the first filters visit does not wait for it.
func warm(ctx *alborz.Context, s *alborz.Login) {
	if !ctx.Server.SieveEnabled(s.Domain()) {
		return
	}
	log := ctx.Logger()
	go func() {
		err := s.DoSieve(func(c alborz.SieveClient) error {
			_, err := c.ListScripts()
			return err
		})
		if err != nil {
			log.Printf("warm %s filters: %v", s.Username(), err)
		}
	}()
}
