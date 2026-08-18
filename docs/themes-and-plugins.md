# Themes

The alborz theme is embedded in the binary. A theme in `themes/<name>/`
overrides it file by file, so it only carries the files it changes.

Templates in `themes/<name>/*.html` override embedded and plugin templates.
Assets are served at `/assets/*`, from `themes/<name>/assets/*` when the
file exists on disk and from the embedded theme otherwise.

# Plugins

Plugins can be written in Go or in Lua.

Go plugin templates and assets are embedded in the binary; files in
`plugins/<name>/public/` override them one by one. Lua plugins live on
disk in `plugins/<name>/`. Assets are served at
`/plugins/<name>/assets/*`.

## Go plugins

They can use the [Go plugin helpers] and need to be included at compile-time in
`cmd/alborz/main.go`.

## Lua plugins

The entry point is at `plugins/<name>/main.lua`.

API:

* `alborz.on_render(name, f)`: prior to rendering the template `name`, call
  `f` with the template data (the special name `*` matches all templates)
* `alborz.set_filter(name, f)`: set a template function
* `alborz.set_route(method, path, f)`: register a new HTTP route, `f` will be
  called with the HTTP context
