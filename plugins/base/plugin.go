package alborzbase

import (
	"embed"
	"time"

	"git.mehdix.org/alborz"
)

//go:embed all:public
var public embed.FS

// UserLocation resolves the user's timezone the same way display does:
// explicit setting first, then the browser-set cookie, else UTC.
func UserLocation(ctx *alborz.Context) *time.Location {
	settings, err := LoadSettings(ctx.Session.Store())
	if err != nil {
		settings = &Settings{}
	}
	return locationOf(ctx, settings)
}

// locationOf is UserLocation for a caller that already holds the
// settings.
func locationOf(ctx *alborz.Context, settings *Settings) *time.Location {
	tz := settings.Timezone
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

func init() {
	p := alborz.GoPlugin{Name: "base", Files: public}

	p.TemplateFuncs(templateFuncs)
	registerRoutes(&p)

	// Inject timezone into all templates
	p.Inject("*", func(ctx *alborz.Context, data alborz.RenderData) error {
		if ctx.Session == nil {
			return nil
		}
		settings, err := LoadSettings(ctx.Session.Store())
		if err != nil {
			return nil // Don't fail render on settings error
		}
		// Unset means times keep their stored zones.
		if loc := locationOf(ctx, settings); loc != time.UTC {
			data.Global().Timezone = loc
		}
		data.Global().FirstDayOfWeek = settings.FirstDayOfWeek
		return nil
	})

	// An account restored from remembered credentials is signed in
	// without passing through the login handler, so the root package
	// calls this to have it followed too.
	loader := p.Loader()
	alborz.RegisterPluginLoader(func(s *alborz.Server) ([]alborz.Plugin, error) {
		s.OnAccountReady = append(s.OnAccountReady, watchAccount, warmAccount)
		s.OnAccountGone = append(s.OnAccountGone, forgetAccount)
		return loader(s)
	})
}

// forgetAccount drops everything held for an account once it is gone:
// its listings and sidebar, the people it writes to, its folder roles
// and what its server calls itself. A cache with nobody behind it is a
// leak, and until now only the listings were let go.
func forgetAccount(username string) {
	listings.evictAll(username)
	bodies.forget(username)
	correspondents.Forget(username)
	unifiedFolders.Forget(username)
	authServGuesses.Forget(username)
}
