# ADR 13: htmx as progressive enhancement

Status: accepted, amended by ADR 96 (navigation history replaces list
refinements and neighbour steps).

## Context

Every page is a server-rendered document: links are GETs, actions are
forms, and every answer is a whole page. Full reloads cost scroll
position, focus and a blank frame per navigation, while the application
must continue working without JavaScript.

## Decision

1. htmx 2 (vendored in `templates/assets/htmx.js`) is loaded on every
   page to fetch server responses and swap them in place.
2. Every route MUST answer a full page to a plain request. A request
   with `HX-Request: true` (and not a history restore) MAY be answered
   with a fragment that is a template block the full page also renders.
   No route MAY exist only for scripts or change semantics for them.
3. Behaviour is declared via `hx-*` attributes on links and forms that
   already work without script.
4. `hx-on:` and `js:` values MUST NOT be used. `allowEval` is `false`
   (`htmx-config` meta), so the Content-Security-Policy needs no
   `unsafe-eval`.
5. Navigation is boosted by `hx-boost="true"` on `body`, with history
   push/replace governed by ADR 96. `historyCacheSize` is `0` so mail is
   never written to `sessionStorage`; the page keeps up to 10 left pages
   in memory for instant Back rendering before revalidation.
6. Forms MUST NOT be boosted (`hx-boost="false"`) when:
   - they carry an `id` for external submit buttons using `form=` and
     `formaction`;
   - they change root-element attributes (`lang`, `dir`, colour scheme,
     text size).
7. Fragments (`Partial`, `PartialFor`) are used only for single-row
   marks, list swaps into `#main` under an unchanged toolbar or rail
   (carrying notices and rail tick updates out of band), and lazy pieces
   (signature verification, status bar, month weather).
   - Actions that remove a row from the current view answer with
     `HX-Location` to refresh `#main` without pushing history.
   - Sign-in and unlock redirects answer fragment requests with
     `HX-Redirect`.
   - Error responses keep their HTTP status and are not swapped by htmx.
8. Compose, imports and file uploads submit without htmx.
9. Controls specific to scripted or unscripted browsing MUST be shown or
   hidden by CSS `@media (scripting: enabled)` or `(scripting: none)`,
   never toggled by script after paint.

## Consequences

- Without JavaScript, every link and form works as a full-page request.
- Every fragment is a block of its full page template, preventing drift.

## References

- CSP Level 3 (`unsafe-eval`); Media Queries Level 5 (`scripting`)
- ADR 8, ADR 14, ADR 41, ADR 96
- `templates/layout.html`, `templates/assets/helpers.js`,
  `templates/assets/history.js`, `web/htmx.go` (`Partial`,
  `PartialFor`), `web/renderer.go`, `web/server.go` (`Relocate`,
  `Redraw`), `web/middleware.go` (CSP)
