package alborzsieve

import (
	"git.mehdix.org/alborz"
)

// Mount serves the filters.
func Mount(srv *alborz.Server) {
	registerRoutes(srv.Router())
	srv.OnAccountReady = append(srv.OnAccountReady, warm)
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
