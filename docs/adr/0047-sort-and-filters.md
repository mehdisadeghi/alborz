# ADR 47: Sort links keep list filters

Status: accepted.

## Context

The task and contact lists are shaped by query parameters (account
scope, collection, query, view, group, category, sort, page, page
size). Each list declares them once: `taskListParams`,
`contactListParams`. Sort links built from the sort alone dropped the
rest.

## Decision

1. `pimweb.Sort` receives the list's parameter slice and keeps every
   parameter in it except `sort`, `dir` and `page` (`sortDrops`).
2. A new order starts at the first page.
3. The parameter slice is the single source for row links, the filter
   menu and sort links; a filter added to it is carried by all.

## Consequences

- Sorting, and the reset-sort link, leave a filtered list filtered.
- A filter is cleared only by its own control, the view that clears
  it, or leaving the list.

## References

- ADR 1, ADR 36, ADR 61
- `web/liststate.go`, `pim/web/sort.go`,
  `pim/calendar/web/tasks.go`, `pim/contacts/web/contacts.go`
