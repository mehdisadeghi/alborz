# ADR 96: Navigation history follows the markup

Status: accepted. Amends ADR 13.

## Context

A boosted request (`hx-boost`, ADR 13) pushes a history entry by
default, so filter, sort, page and view steps and actions would fill
history, and Back would retrace controls instead of returning to the
previous page.

## Decision

1. A navigation to another page (a link, a rail folder or collection,
   an object, an editor, a form's landing page) MUST push a history
   entry. No script intercepts clicks or rewrites history entries.
2. A refinement that redraws the current page in place (filter, group,
   sort, per-page, page and month steps, toolbar search, rail ticks, a
   view toggle, an object's previous or next neighbour, a message's
   part tabs) MUST declare `hx-replace-url="true"`; the shared `swaps`
   and `toolbar-search` defines in `templates/partials/toolbar.html`
   carry it.
3. An action (star, colour, task done) MUST NOT add a history entry: its
   button posts with `swap-main` and redraws `#main` from the page the
   form names in `next`, which is the page the action was taken on.
   Without script the form posts whole and redirects to that page's
   request URI, query included.
4. Native Back, Forward, modified clicks and new tabs MUST NOT be
   intercepted.

## Consequences

- With script, Back always returns to the previous page the reader
  navigated to, across sections and in the PWA.
- Without script, every action adds an entry for the same page: a
  browser pushes any navigation that carries a request body, even to
  the current URL, so Back first shows the page as it was before the
  action.
- Back after saving an editor returns to the editor, as in any
  multi-page site.
- No state is kept in `history.state`.

## References

- HTML Living Standard, session history; navigate (historyHandling
  becomes `replace` for a same-URL navigation only without a request
  body)
- ADR 13, ADR 36, ADR 37
- `templates/partials/toolbar.html` (`swaps`, `toolbar-search`,
  `object-step`, `toggle`, `swap-main`), `templates/partials/star.html`,
  `templates/partials/rail.html` (`list-swap`),
  `templates/pages/tasks/task.html`, `templates/pages/mail/message.html`
