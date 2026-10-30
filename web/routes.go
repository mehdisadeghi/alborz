package web

import (
	"errors"
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
	// The launcher uses the public theme cookie; a document supplies its resolved scheme after CSS loads.
	g.GET("manifest", "/manifest.webmanifest", func(ctx *Context) error {
		worker := ctx.Path("core.worker")
		theme := s.base
		if name, ok := ctx.cookieValue(themeCookieName, s.hasTheme); ok {
			for _, candidate := range s.themes {
				if candidate.Name == name {
					theme = candidate
					break
				}
			}
		}
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
			"name":       alborz.BrandName,
			"short_name": alborz.BrandName,
			"start_url":  ctx.Path("mail.home"),
			"scope":      strings.TrimSuffix(worker, path.Base(worker)),
			"display":    "standalone",
			// A manifest has one colour: pinned themes are exact; adaptive themes use their light
			// fallback until the document's computed theme-color takes over.
			"theme_color":      theme.Colors[0].Color,
			"background_color": theme.Colors[0].Color,
			"icons": []map[string]string{
				icon("icon-192.png", ""),
				icon("icon-512.png", ""),
				icon("icon-maskable-512.png", "maskable"),
			},
			"share_target": map[string]any{
				"action": ctx.Path("mail.share"), "method": "POST", "enctype": "multipart/form-data",
				"params": map[string]any{"title": "title", "text": "text", "url": "url",
					"files": []map[string]any{{"name": "files", "accept": []string{"*/*"}}}},
			},
			// Where the Settings buttons point, so installing offers the app for these links.
			"protocol_handlers": []map[string]string{
				{"protocol": "mailto", "url": ctx.Path("mail.compose") + "?mailto=%s"},
				{"protocol": "webcal", "url": ctx.Path("calendar.import") + "?url=%s"},
			},
		})
	})

	// Served here rather than under /assets/: a worker's scope is the path it is served from.
	g.GET("worker", "/sw.js", func(ctx *Context) error {
		urls, version := s.shell()
		var downloads []string
		for _, pattern := range s.routes.downloads {
			downloads = append(downloads, pathRegexp(pattern))
		}
		config := mustJSON(map[string]any{
			"version": version, "shell": urls, "downloads": downloads,
			"signings": []string{ctx.Path("auth.login"), ctx.Path("auth.logout"), ctx.Path("auth.unlock.leave"), ctx.Path("settings.sessions")},
			"signIn":   ctx.Path("auth.login"), "assets": ctx.Path("core.asset", ""), "icon": s.AssetURL("icon-192.png"),
			"pushKey": s.Pushes.Key.Public(), "renew": ctx.Path("settings.notifications.create"), "shellHeader": shellHeader,
		})
		return ctx.Blob(http.StatusOK, "text/javascript; charset=utf-8", []byte("const CONFIG = "+config+";\n"+serviceWorker))
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
		files, file, ok := s.assetSource(name)
		if !ok {
			return NewError(http.StatusNotFound)
		}
		assets, err := fs.Sub(files, "assets")
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return NewError(http.StatusNotFound)
			}
			return err
		}
		http.ServeFileFS(ctx.Response(), ctx.Request(), assets, strings.TrimPrefix(file, "assets/"))
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
