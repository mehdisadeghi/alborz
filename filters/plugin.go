package filters

import (
	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

// Mount serves the filters.
func Mount(srv *web.Server) {
	registerRoutes(srv.Router())
	srv.OnAccountReady = append(srv.OnAccountReady, warm)
}

// warm dials ManageSieve at sign-in so the first filters visit does not wait for it.
func warm(ctx *web.Context, s *alborz.Login) {
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
