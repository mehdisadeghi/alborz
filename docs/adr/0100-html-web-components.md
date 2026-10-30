# ADR 100: HTML web components

Status: accepted.

## Context

Scripts enhance server-rendered markup by searching the document after
load and again after every htmx swap, and share functions through
globals. Missed re-runs leave swapped content unenhanced, listeners
leak, and nothing in the markup names the script that acts on it.

## Decision

1. Scripted behaviour for a piece of markup MUST be an autonomous custom
   element (`<name-part>`) wrapping server HTML that already works
   without script (ADR 13).
2. Elements MUST use the light DOM. Shadow DOM and customized built-in
   elements (`is=`) MUST NOT be used.
3. Behaviour MUST be attached in `connectedCallback` and removed in
   `disconnectedCallback`; the element MUST NOT depend on a document
   scan or on swap events to start.
4. Without JavaScript, or before definition, the wrapped markup MUST
   work unchanged. An undefined element only groups its content.
5. Each element is defined in one file named after its tag, beside the
   partial that renders it: `templates/partials/x.html` ↔
   `templates/partials/x.js`, and `x.css` (ADR 99) when it has styles.
6. Scripts MUST NOT share functions or state through globals. An
   element MAY expose methods and dispatch DOM events; other code uses
   those.
7. State shared between elements MUST live in the markup (attributes)
   or the server, not in script variables.

## Consequences

- Content swapped in by htmx is enhanced by the browser; no
  re-enhance step exists.
- The tag name in the markup names the file holding its script.
- Each element costs one wrapper in the markup.

## References

- WHATWG HTML Living Standard, Custom elements
- ADR 8, ADR 13, ADR 55, ADR 99
- Elements to define, by current source:
  - `toolbar-fit`, `overflow-menu`, `bulk-select`, `linked-select`,
    `mail-rail`, `status-segment`, `star-toggle`, `notice-stack`,
    `folder-drop`, `site-search`: `templates/assets/helpers.js`
  - `recipient-input`: `templates/assets/recipients.js`
  - `compose-drop`, `compose-body`: `templates/assets/compose.js`,
    `templates/assets/helpers.js` (`data-caret`)
- `templates/assets/session-lock.js`
