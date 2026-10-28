# ADR 48: No scheme control for single-scheme themes

Status: accepted.

## Context

A theme written for one colour scheme offers no light/dark choice, yet
the header drew a disabled toggle for it.

## Decision

1. A theme pins a scheme when its CSS sets `color-scheme: light` or
   `dark` and has no `prefers-color-scheme: dark` query or
   `[data-theme="dark"]` rule.
2. For a pinned theme the page MUST NOT render the scheme toggle.
3. The settings theme chooser names the scheme a theme pins.

## Consequences

- No disabled control or explanatory popover.

## References

- ADR 50
- `web/prefs.go` (`pinnedScheme`, `readThemes`, `themeScheme`),
  `web/renderer.go` (`SchemePinned`), `templates/partials/nav.html`
  (`scheme-toggle`), `templates/pages/settings/general.html`
