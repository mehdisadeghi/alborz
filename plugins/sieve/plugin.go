package alpssieve

import (
	"git.sr.ht/~migadu/alps"
)

func init() {
	alps.RegisterPluginLoader(func(srv *alps.Server) ([]alps.Plugin, error) {
		if !srv.SieveEnabled() {
			return nil, nil
		}

		p := alps.GoPlugin{Name: "sieve"}
		registerRoutes(&p)
		return []alps.Plugin{p.Plugin()}, nil
	})
}
