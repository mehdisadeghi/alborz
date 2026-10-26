# ADR 96: Navigation history follows the workflow

Status: accepted. Amends ADR 13.

## Context

Boosting every link adds a history entry for every filter, page and neighbouring
message. Browser Back then retraces controls instead of returning to the list.
An editor and its saved object also produce duplicate entries.

## Decision

1. List refinements within mail, calendar, tasks, journal, contacts or search
   MUST replace the current entry. The URL still records the selected folder,
   account, date, query, filter, order and page.
2. Opening an object MUST push an entry. Its view changes and previous/next
   controls MUST replace that entry. Other object links, including subtasks,
   MUST retain their steps.
3. Opening an existing object's editor and returning to that same owned object
   through a boosted save MUST replace the entry. New-object forms and full-page
   submissions retain ordinary browser navigation.
4. An unmodified same-tab link from an object or editor to its exact recorded
   immediate parent MAY use browser Back. The parent MUST come from an actual
   htmx push, never from a supplied `from` parameter or an assumed default page.
5. Browser Back and Forward MUST NOT be redirected or intercepted. Direct links,
   modified clicks, new tabs and navigation without scripting remain native.
   Section changes remain history steps; no synthetic home entry is inserted.

## Consequences

- Back from a sequence of neighbouring objects returns to the latest list state.
- Browser history retains a parent URL, but no additional page contents.
- App Up is a destination link. It can reuse a known parent entry; a bookmark or
  a link to a different list still navigates normally.
- This does not impose a native-app default-page-then-exit stack. Unknown routes
  and ordinary full-page form submissions retain browser history semantics.

## References

- ADR 13, ADR 36, ADR 37
- `templates/assets/history.js`, `tests/history.test.cjs`
- https://htmx.org/events/#htmx:beforeHistoryUpdate
- https://developer.mozilla.org/en-US/docs/Web/API/History/replaceState
