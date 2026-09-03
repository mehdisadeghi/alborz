# Themes

The alborz theme is embedded in the binary. A theme in `themes/<name>/`
overrides it file by file, so it only carries the files it changes.

Templates in `themes/<name>/*.html` override embedded and plugin templates.
Assets are served at `/assets/*`, from `themes/<name>/assets/*` when the
file exists on disk and from the embedded theme otherwise.

# Plugins

Plugins are written in Go. Their templates and assets are embedded in
the binary; files in `plugins/<name>/public/` override them one by
one. Assets are served at `/plugins/<name>/assets/*`.

They can use the [Go plugin helpers] and need to be included at compile-time in
`cmd/alborz/main.go`.
