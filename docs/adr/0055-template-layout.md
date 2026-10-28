# ADR 55: Template layout and page clones

Status: accepted.

## Context

html/template silently lets a second definition of a name replace the
first, so in one shared name space a page can change a partial for
every page. A `{{template}}` naming nothing fails only when executed.

## Decision

1. Templates live in one tree, `templates/`: `layout.html`,
   `partials/`, `pages/`, `assets/`. It is embedded in the binary. With
   `-debug` it is read from `./templates` on disk. SIGUSR1 re-parses
   the templates and `custom.css`.
2. `layout.html` defines the page and its blocks: `body`, `rail`,
   `before-main`, `main-class`, `main`, `scripts`. The layout and every
   partial are parsed into one set.
3. Each file under `pages/` is parsed into its own clone of that set,
   named by its path without `pages/` and `.html`. A page MAY define
   only the layout's blocks or a partial's empty block; its
   definitions stay in its clone.
4. A page renders by executing `layout`. Any other name is a partial,
   rendered alone as a fragment (ADR 13).
5. Loading MUST fail on:
   - a name defined twice among the layout and partials;
   - a page redefining a non-empty partial;
   - a `{{template}}` naming no template;
   - once routes are mounted, a `url` call naming no route or given the
     wrong number of values (ADR 66).
   A reload that fails keeps the templates in force. A `url` whose
   route name is computed at run time is not checked.
6. `missingkey` keeps html/template's default; `missingkey=error` would
   make every optional key mandatory.

## Consequences

- A page cannot change what another page renders.
- Shared markup is a partial or the layout; a page holds only its own.
- A release needs no template files beside the binary.
- A broken template is a failed start, not a 500.

## References

- ADR 13, ADR 66
- `web/renderer.go` (`loadTemplates`, `resolved`, `renderer.Render`),
  `web/router.go` (`checkURLs`), `web/server.go` (`load`, `Reload`),
  `templates/layout.html`, `embed.go`, `cmd/alborz/main.go` (SIGUSR1)
