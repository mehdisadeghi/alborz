package web

import (
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"git.mehdix.org/alborz"
)

// PublicRoutes serve what a browser fetches before signing in: they say nothing about any account.
func PublicRoutes(g *Group) {
	g = g.Group("", "core")
	s := g.s
	// Generated so the name under the icon is BrandName.
	g.GET("manifest", "/manifest.webmanifest", func(ctx *Context) error {
		icon := func(name, purpose string) map[string]string {
			out := map[string]string{"src": s.AssetURL(name), "sizes": "512x512", "type": "image/png"}
			if strings.HasSuffix(name, "192.png") {
				out["sizes"] = "192x192"
			}
			if purpose != "" {
				out["purpose"] = purpose
			}
			return out
		}
		ctx.Response().Header().Set("Content-Type", "application/manifest+json")
		return ctx.JSON(http.StatusOK, map[string]any{
			"name":             alborz.BrandName,
			"short_name":       alborz.BrandName,
			"start_url":        "/",
			"scope":            "/",
			"display":          "minimal-ui",
			"background_color": brandBackground,
			"theme_color":      brandColor,
			"icons": []map[string]string{
				icon("icon-192.png", ""),
				icon("icon-512.png", ""),
				icon("icon-maskable-512.png", "maskable"),
			},
		})
	})

	// Served here rather than under /assets/: a worker's scope is the path it is served from.
	g.GET("worker", "/sw.js", func(ctx *Context) error {
		urls, version := s.shell()
		body := fmt.Sprintf(serviceWorker, version, mustJSON(urls))
		return ctx.Blob(http.StatusOK, "text/javascript; charset=utf-8", []byte(body))
	})

	g.GET("asset", "/assets/{name...}", func(ctx *Context) error {
		name := strings.TrimPrefix(path.Clean("/"+ctx.Param("name")), "/")
		if name == "" {
			return NewError(http.StatusNotFound)
		}
		if v := ctx.QueryParam("v"); v != "" && v == s.assetStamp(name) {
			ctx.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		if name == customCSS {
			custom := s.loaded.Load().custom
			if len(custom) == 0 {
				return NewError(http.StatusNotFound)
			}
			return ctx.Blob(http.StatusOK, "text/css; charset=utf-8", custom)
		}
		assets, err := fs.Sub(s.files, "assets")
		if err != nil {
			return err
		}
		http.ServeFileFS(ctx.Response(), ctx.Request(), assets, name)
		return nil
	})
}

// EventRoutes stream what the watchers see to open pages, in a group of their own: a stream is
// neither compressed, which holds it back, nor the reader acting.
func EventRoutes(g *Group) {
	g.Group("", "core").GET("events", "/events", handleEvents)
}

// Fallback answers every path no route serves.
func Fallback(g *Group) {
	g.add("", "", "/", g.s.unrouted)
}
