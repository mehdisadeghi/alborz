# ADR 82: Add-ons as build options

Status: accepted. Amends ADR 67, ADR 77, ADR 78 and ADR 79.

## Context

Keeping add-on templates, assets and locale strings in the shared
`templates/` and `locales/` trees forced every binary to carry all
add-ons and their outbound clients, and prevented building the core
alone because startup checks every template's routes (ADR 55, ADR 66).

## Decision

1. Each add-on MUST live in a self-contained package tree under
   `addons/<name>/` embedding its own code, templates, assets and locale
   files; nothing belonging to an add-on may live in the shared
   `templates/` or `locales/` directories.
2. Each add-on registers itself from `cmd/alborz/app/addon_<name>.go`
   guarded by `//go:build !no<name> && !noaddons`. `routes.go` MUST NOT
   name any add-on.
3. The core MUST expose registration seams only: status segments
   (`web.Segment`), page add-ons (`web.Addon`), settings rail entries,
   page extensions (`web.Extend`) and locale bundles
   (`locale.RegisterFS`).
4. At startup the renderer and locale catalog merge each registered
   add-on's embedded files. A build omitting an add-on carries none of
   its routes, templates, strings or network clients.
5. `go build -tags noaddons ./cmd/alborz` builds the core alone;
   `-tags no<name>` omits individual add-ons. Tests run both with all
   add-ons and with `-tags noaddons`.
6. Stored settings belonging to an omitted add-on MUST remain preserved
   untouched in the store (ADR 26, ADR 59).

## Consequences

- Default builds include all add-ons; `-tags noaddons` produces a
  core-only binary (`alborz-core`).
- Startup template route verification works with any combination of
  add-on build tags.

## References

- ADR 26, ADR 55, ADR 59, ADR 66, ADR 67, ADR 77, ADR 78, ADR 79
- `cmd/alborz/app/addon_*.go`, `cmd/alborz/app/noaddons_test.go`,
  `web/addons.go`, `web/status.go`, `internal/locale/locale.go`
  (`RegisterFS`)
