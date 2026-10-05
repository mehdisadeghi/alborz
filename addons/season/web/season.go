// Package web lets the season fall on the pages of a reader who turns it on: snow, or what the theme names (ADR 77).
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

//go:embed kinds
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
	s.Addons = append(s.Addons, web.Addon{Name: "season", Script: "season.script", Settings: "season.settings"})
}
