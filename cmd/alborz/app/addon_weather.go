//go:build !noweather && !noaddons

package app

import (
	"git.mehdix.org/alborz/addons/weather"
	weatherlocales "git.mehdix.org/alborz/addons/weather/locales"
	weatherweb "git.mehdix.org/alborz/addons/weather/web"
	"git.mehdix.org/alborz/internal/locale"
	"git.mehdix.org/alborz/web"
)

func init() {
	if err := locale.RegisterFS(weatherlocales.FS); err != nil {
		panic(err)
	}
	addonRegistrations = append(addonRegistrations, func(g *web.Group, options Options) error {
		var sources weather.Sources
		if value, ok := options.Addons["weather"]; ok {
			sources = value.(weather.Sources)
		}
		weatherweb.Mount(g.Group("", "weather"), weather.New(sources, g.Server().Outgoing.Trusted, options.ProjectURL))
		return nil
	})
}
