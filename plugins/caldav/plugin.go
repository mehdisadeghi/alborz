package alborzcaldav

import (
	"embed"
	"git.mehdix.org/alborz/plugins/davcache"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-webdav/caldav"
)

//go:embed all:public
var public embed.FS

const (
	inputDateLayout     = "2006-01-02"
	inputDateTimeLayout = "2006-01-02T15:04"
)

type plugin struct {
	alborz.GoPlugin
	dav *dav.Provider
}

func (p *plugin) client(session *alborz.Session) (*caldav.Client, error) {
	u, ok := p.dav.URL(session)
	if !ok {
		return nil, errNoCalendar
	}
	return newClient(u, p.dav.HTTPClient(session))
}

func newPlugin(srv *alborz.Server) (alborz.Plugin, error) {
	provider, err := dav.NewProvider(srv, dav.Kind{
		Name: "caldav", Label: "CalDAV",
		Schemes: [2]string{"caldavs", "caldav+insecure"},
		// Invitations and tasks ticked on a phone should show within
		// minutes.
		Poll:     davcache.DefaultPoll,
		Discover: caldav.DiscoverContextURL,
		FindHome: findHome,
		List:     listCalendars,
		Make:     doMkcalendar,
		Unnamed:  "calendar",
	})
	if err != nil || provider == nil {
		return nil, err
	}

	p := &plugin{
		GoPlugin: alborz.GoPlugin{Name: "caldav", Files: public},
		dav:      provider,
	}
	p.EnabledFunc = provider.Enabled
	p.CloseFunc = provider.Close

	registerRoutes(p)
	p.registerScheduling()
	srv.OnAccountReady = append(srv.OnAccountReady, p.warm)

	return p.Plugin(), nil
}

func init() {
	alborz.RegisterPluginLoader(func(s *alborz.Server) ([]alborz.Plugin, error) {
		p, err := newPlugin(s)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, nil
		}
		return []alborz.Plugin{p}, err
	})
}
