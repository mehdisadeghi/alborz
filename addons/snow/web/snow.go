// Package web lets it snow on the pages of a reader who turns it on.
package web

import (
	_ "embed"
	"net/http"

	"git.mehdix.org/alborz/web"
)

//go:embed snow.js
var script []byte

// Mount serves the script and offers snow to every reader.
func Mount(g *web.Group) {
	g.GET("script", "/snow.js", func(ctx *web.Context) error {
		return ctx.Blob(http.StatusOK, "text/javascript; charset=utf-8", script)
	})
	s := g.Server()
	s.Addons = append(s.Addons, web.Addon{Name: "snow", Script: "snow.script"})
}
