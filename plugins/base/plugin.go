package alpsbase

import (
	"time"

	"git.sr.ht/~migadu/alps"
)

// UserLocation resolves the user's timezone the same way display does:
// explicit setting first, then the browser-set cookie, else UTC.
func UserLocation(ctx *alps.Context) *time.Location {
	tz := ""
	if settings, err := LoadSettings(ctx.Session.Store()); err == nil {
		tz = settings.Timezone
	}
	if tz == "" {
		if c, err := ctx.Cookie("timezone"); err == nil {
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
	p := alps.GoPlugin{Name: "base"}

	p.TemplateFuncs(templateFuncs)
	registerRoutes(&p)

	// Inject timezone into all templates
	p.Inject("*", func(ctx *alps.Context, data alps.RenderData) error {
		if ctx.Session == nil {
			return nil
		}
		// Unset means times keep their stored zones.
		if loc := UserLocation(ctx); loc != time.UTC {
			data.Global().Timezone = loc
		}
		return nil
	})

	alps.RegisterPluginLoader(p.Loader())
}
