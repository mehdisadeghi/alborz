# ADR 99: CSS architecture

Status: proposed.

## Context

CSS sources are bundled from shared primitives and component-owned files,
with responsive variants beside the components they adjust. Equal-specificity
rules still depend on import order, and viewport breakpoints still govern
component layouts. Layer precedence, intrinsic layouts and container queries
would make those dependencies explicit.

## Decision

### Baseline

1. Stylesheets MUST work in Chrome 99, Firefox 97 and Safari 15.4 or
   later, the first releases supporting `@layer` and `@import … layer()`.
2. Features newer than the baseline MAY be used only when the bundler
   lowers them for it (native nesting) or when browsers lacking them
   still get a usable page.

### Sources

3. The cascade layers are declared once, in this order: `tokens`,
   `base`, `layout`, `components`, `sections`, `utilities`. Every rule
   MUST belong to one of them.
4. The entry stylesheet in `templates/assets/css/` lists every source
   with `@import url(…) layer(…)`; it holds no rules.
5. Tokens, base element styles and layout primitives live in
   `templates/assets/css/`.
6. A partial's styles live beside it with the same name:
   `templates/partials/x.html` ↔ `templates/partials/x.css`. Add-on
   styles live in the add-on's tree (ADR 82).
7. A page MUST NOT restyle a component for itself; a variant belongs in
   the component's file.
8. A component's media and container queries MUST be nested inside the
   component's rule.

### Layout

9. Layout SHOULD be intrinsic: the Switcher (all items in one row while
   they fit, all stacked otherwise), Cluster, Sidebar and auto-fit grid
   primitives.
10. A component that adapts to its width MUST use container queries.
11. Media queries are reserved for `prefers-color-scheme`,
    `prefers-reduced-motion`, `pointer`, `scripting` (ADR 13) and
    `print`. Viewport width queries MAY remain only for the page shell.

### Delivery

12. The server MUST assemble the sources listed in
    `templates/assets/css/index.css` into one version-stamped
    `style.css` stylesheet in memory, re-reading modified sources on
    reload in `-debug` mode (ADR 55). No external build step or Node
    toolchain is required.
13. The service worker's precache list MUST include the stamped
    `style.css` URL.
14. Rules matching no markup MUST be removed.

## Consequences

- Where a rule lives follows from the partial it styles.
- Layer order, not selector weight, settles conflicts between groups.
- Browsers older than the baseline ignore layered rules and get an
  unstyled page.

## References

- CSS Cascading and Inheritance Level 5 (`@layer`, `@import layer()`);
  CSS Nesting Module; CSS Containment Module Level 3 (container
  queries); Media Queries Level 5
- ADR 13, ADR 55, ADR 82
- `templates/assets/css/index.css`, `templates/partials/`,
  `web/prefs.go` (`bundleStylesheet`), `web/server.go` (`shellAssets`,
  `styleCSS`), `web/serviceworker.js`
