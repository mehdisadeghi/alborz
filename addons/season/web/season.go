// Package web serves the seasonal valley scene partials (--scene layers: far,
// conifers/broadleaves, blossoms, near, house, smoke, snowcover, foreground)
// and the opt-in falling-weather engine (--season-weather: snow, leaves, rain,
// sunrays) at shared scale METRE = 53 CSS px/m:
//   - snow (kinds/snow.js): Reiter crystal sprites, Locatelli-Hobbs terminal
//     speeds, Gunn-Marshall sizes, and Tycho-2 winter night star density.
//   - leaves (kinds/leaves.js): RK4 integration of the Andersen, Pesavento &
//     Wang quasi-steady plate ODE (J. Fluid Mech. 541, 2005, 65-104) at
//     half-chord a = 4 cm across fluttering, tumbling and chaotic regimes for
//     four Hyrcanian leaf outlines (Parrotia persica, Acer velutinum, Fagus
//     orientalis, Quercus castaneifolia).
//   - rain (kinds/rain.js): Marshall-Palmer (J. Meteorol. 5, 1948) drop sizes
//     at Atlas-Srivastava-Sekhon (Rev. Geophys. 11, 1973) terminal speeds.
//   - sunrays (kinds/sunrays.js): soft inline-end wedges at half resolution.
package web

import (
	"embed"
	"io/fs"
	"net/http"

	"git.mehdix.org/alborz/web"
)

//go:embed season.js
var script []byte

//go:embed settings.js
var settings []byte

//go:embed assets partials kinds
var embedded embed.FS

// Mount serves the script and the kinds it falls as, and offers the season to every reader.
func Mount(g *web.Group) {
	kinds, err := fs.Sub(embedded, "kinds")
	if err != nil {
		panic(err)
	}
	g.GET("script", "/season.js", func(ctx *web.Context) error {
		return ctx.Blob(http.StatusOK, "text/javascript; charset=utf-8", script)
	})
	g.GET("settings", "/season-settings.js", func(ctx *web.Context) error {
		return ctx.Blob(http.StatusOK, "text/javascript; charset=utf-8", settings)
	})
	// A kind is a module the script imports by the name the theme gives, so a new kind is a file in kinds/.
	g.GET("kind", "/season/{name}", func(ctx *web.Context) error {
		http.ServeFileFS(ctx.Response(), ctx.Request(), kinds, ctx.Param("name"))
		return nil
	})
	s := g.Server()
	if err := s.RegisterAddonBundle("season", embedded); err != nil {
		panic(err)
	}
	s.Addons = append(s.Addons, web.Addon{Name: "season", Script: "season.script", Settings: "season.settings"})
}
