# ADR 50: Colour scheme toggle is per tab

Status: accepted.

## Context

The page follows `prefers-color-scheme`. The reader MAY override it
light or dark. A server-side cookie outlives the conditions the
override was made for and is state the server holds per browser.

## Decision

1. The override MUST be stored in `sessionStorage` (key `scheme`) and
   applied as `data-theme` on the root element. It MUST NOT reach the
   server.
2. `scheme.js` MUST load blocking in `<head>`, before the stylesheets,
   and set `data-theme` to the stored scheme or `system` before first
   paint. It is a file, not inline, because the CSP allows no inline
   script. It is part of the shell (`shellAssets`).
   Its `applyScheme`, also run by the toggle, switches which
   `theme-color` meta applies (ADR 14 rule 9).
3. The toggle renders all three buttons (`system`, `light`, `dark`);
   CSS shows the one matching `data-theme`. Each click moves to the
   next and keeps focus on the shown button.
4. A theme that pins one scheme hides the toggle (`SchemePinned`,
   ADR 48). A permanent choice is such a theme.

## Consequences

- No `/scheme` route, no scheme cookie, no scheme in the render data.
- Without JavaScript no button matches and the toggle is hidden.
- A new tab starts from the system scheme, unless the browser copies
  `sessionStorage` into a tab opened from this one.

## References

- ADR 14, ADR 48
- `templates/assets/scheme.js`, `templates/assets/helpers.js`
  (`.scheme-toggle` click handler), `templates/layout.html`,
  `templates/partials/nav.html` (`scheme-toggle`),
  `templates/assets/style.css` (`.scheme-toggle`),
  `web/server.go` (`shellAssets`), `web/middleware.go` (CSP)
