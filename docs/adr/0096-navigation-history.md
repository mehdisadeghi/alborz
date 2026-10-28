# ADR 96: Navigation history follows the workflow

Status: accepted. Amends ADR 13.

## Context

Pushing a browser history entry on every boosted link (`hx-boost`,
ADR 13) fills history with intermediate filter, sort, page and
next/previous steps, so pressing Back retraces controls instead of
returning to the list.

## Decision

1. List refinements within mail, calendar, tasks, journal, contacts or
   search (changing folder, account, date, query, filter, sort or page)
   MUST use `history.replaceState` while updating the URL.
2. Opening an object from a list MUST push a history entry. Switching
   that object's view (plain text / HTML) or stepping to its previous or
   next neighbour MUST replace the object's entry. Links to distinct
   objects (such as parent/child subtasks) push a new entry.
3. Opening an existing object's editor and saving it back to the same
   object via boosted navigation MUST replace the object's entry.
4. Clicking an unmodified same-tab link from an object or editor whose
   `href` equals the exact parent URL recorded on the entry's htmx push
   (`history.state.alborzParent`) calls `history.back()`. Links to any
   other URL (such as a bookmark's fallback list) navigate normally.
5. Native browser Back and Forward, modified clicks, new tabs, section
   switches and unscripted navigation MUST NOT be intercepted.

## Consequences

- Stepping through several messages or events and pressing Back returns
  directly to the originating list state in one step.
- Only the parent URL string is stored in `history.state`; no message or
  page markup is persisted in `sessionStorage` (ADR 13 rule 5).

## References

- ADR 13, ADR 36, ADR 37
- `templates/assets/history.js`, `tests/history.test.cjs`
