# ADR 13: htmx as progressive enhancement

Status: accepted.

## Context

Every page is a server-rendered document: links are GETs, actions are
forms, and every answer is a whole page. This works without scripting
and MUST keep working. Full reloads cost scroll position, focus and a
blank page per navigation.

## Decision

1. htmx 2 (2.0.10, 0BSD, vendored in `templates/assets/htmx.js`) is
   loaded on every page. Its only job is to fetch what the server
   would send anyway and swap it in place.
2. Every route MUST answer a whole page to a plain request. A request
   with `HX-Request: true` (and not a history restore) MAY be answered
   with a fragment; a fragment MUST be a template block the full page
   also renders. No route MAY exist only for scripts, and no route MAY
   change meaning because a script asked.
3. Behaviour is declared by `hx-*` attributes on the element that
   already works without script. Without htmx the attributes are inert
   and the form or link still works.
4. `hx-on:` and `js:` values MUST NOT be used. `allowEval` is false
   (`htmx-config` meta), so the Content-Security-Policy needs no
   `unsafe-eval`.
5. Navigation is boosted by `hx-boost="true"` on `body`. History is
   pushed with the link's own URL; `historyCacheSize` is 0.
6. Forms MUST NOT be boosted (`hx-boost="false"`) when:
   - they have an `id`, i.e. are submitted by buttons outside them
     with `form=` and `formaction` (htmx drops the button's name and
     value and the `formaction` unless the button has an explicit
     `type`);
   - they change root-element attributes (`lang`, `dir`, colour
     scheme, text size): language and reading settings reload the
     whole page.
7. Fragments are used only where a whole page is the wrong answer: a
   mark on one row, a list swapped into `#main` under an unchanged
   toolbar or rail (a page, a sort, a rail tick), and lazily loaded
   pieces (signature check, status bar).
   A page asked for by a swap into `#main` answers its `main` block;
   a calendar, task or contact page adds its rail's tick form and All
   link out of band in a `template`, so their marks and ticks follow
   the list while the rail and its scroll stay.
   Signing in and unlocking take the window: a piece asked for then is
   answered with `HX-Redirect`. An error keeps its status, which htmx
   does not swap, so the page stays and the failed notice shows.
8. Compose, import and file uploads post without htmx.
9. What serves only a page with scripting, or only one without, is
   shown or hidden by CSS under `@media (scripting: enabled)` or
   `(scripting: none)`. No script MAY reveal or hide it after paint:
   nothing appears or leaves when a script runs or a swap lands.

## Consequences

- Without scripting the interface is unchanged.
- Cost: ~51 KB of script, and the rule that a fragment is a block of
  its page. A fragment the full page does not render will drift from
  it unnoticed.

## References

- htmx 2 documentation (`hx-boost`, `HX-Request`, `HX-Target`)
- CSP Level 3 (`unsafe-eval`)
- Media Queries Level 5 (`scripting`)
- ADR 14, ADR 41
- `templates/layout.html` (`htmx-config`, `body hx-boost`),
  `web/htmx.go` (`Partial`, `PartialFor`), `web/renderer.go`,
  `web/middleware.go` (CSP), `templates/partials/form.html`,
  `templates/assets/VENDOR.toml`
