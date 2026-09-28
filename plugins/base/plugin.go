package alborzbase

import (
	"embed"
	"sync"
	"time"

	"git.mehdix.org/alborz"
)

//go:embed all:public
var public embed.FS

// UserLocation resolves the reader's timezone.
func UserLocation(ctx *alborz.Context) *time.Location {
	tz := ctx.Reading().Timezone
	if tz == "" {
		if c, err := ctx.Cookie(alborz.TimezoneCookieName); err == nil {
			tz = c.Value
		}
	}
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.UTC
}

type plugin struct {
	alborz.GoPlugin
	listings        *listingCache
	bodies          *bodyCache
	accountSidebars *alborz.Memo[sidebar]
	unifiedFolders  *alborz.Memo[map[string]string]
	correspondents  *alborz.Memo[[]string]
	senderBooks     *alborz.Memo[*senderBook]
	authServGuesses *alborz.Memo[string]
	wkd             *wkdCache
	watchers        sync.Map
}

func newPlugin(o *alborz.Options) *plugin {
	bodies := newBodyCache(o.CacheFor(bodyKeepFor))
	sidebars := alborz.NewBackgroundMemo[sidebar](o.CacheFor(listingFreshFor))
	p := &plugin{
		GoPlugin:        alborz.GoPlugin{Name: "base", Files: public},
		listings:        newListingCache(o.CacheFor(listingKeepFor), sidebars, bodies),
		bodies:          bodies,
		accountSidebars: sidebars,
		unifiedFolders:  alborz.NewMemo[map[string]string](o.CacheFor(unifiedFoldersTTL)),
		// Indexes, not cached answers: they stay with caching off.
		correspondents:  alborz.NewMemo[[]string](correspondentTTL),
		senderBooks:     alborz.NewMemo[*senderBook](senderBookTTL),
		authServGuesses: alborz.NewMemo[string](authSampleTTL),
		wkd:             newWKDCache(o.CacheFor(wkdKeepFor)),
	}

	p.TemplateFuncs(templateFuncs)
	p.registerRoutes()

	p.Inject("*", func(ctx *alborz.Context, data alborz.RenderData) error {
		if ctx.Login == nil {
			return nil
		}
		// Unset means times keep their stored zones.
		if loc := UserLocation(ctx); loc != time.UTC {
			data.Global().Timezone = loc
		}
		data.Global().FirstDayOfWeek = ctx.Reading().FirstDayOfWeek
		return nil
	})
	return p
}

func init() {
	// A restored account skips the login handler, so it is followed here too.
	alborz.RegisterPluginLoader(func(s *alborz.Server) ([]alborz.Plugin, error) {
		p := newPlugin(s.Options)
		s.OnAccountReady = append(s.OnAccountReady, p.watchAccount, p.warmAccount)
		s.OnAccountGone = append(s.OnAccountGone, p.forgetAccount)
		return []alborz.Plugin{p.Plugin()}, nil
	})
}
