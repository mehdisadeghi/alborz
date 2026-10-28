# ADR 3: Task selection

Status: accepted, amended by ADR 34 (a fully selected page may widen
to the whole view) and ADR 88 (native `MOVE` within one source, bulk
copy, and conditional cross-source transfer).

## Context

Mail and contacts select rows with a checkbox bound to a page form.
Tasks need the same bulk actions and a separate control to mark a task
completed.

## Decision

1. Every task row MUST start with a square selection checkbox named
   `paths`, bound by `form="tasks-form"`, with the value
   `<account>|<path>`.
2. The toolbar actions that need a selection (mark completed, delete,
   copy, move, download) submit `tasks-form` via `formaction`. There is
   no bulk reopen in the toolbar.
3. The completion toggle is a separate round control on each row that
   POSTs to the task's own `complete` route.
4. Controls of a form that row checkboxes name MUST be disabled by
   script while no row is checked. Without script they stay enabled, and
   an empty submit redirects back.
5. A selection MAY span accounts. `Selected` resolves each
   `account|path` to its account's client, one client per account.
   Calendars, tasks, journals and contacts share it.
6. Moving or copying tasks follows ADR 88: native conditional `MOVE`
   within one DAV source, or conditional `PUT` (`If-None-Match: *`)
   followed by conditional `DELETE` (`If-Match`) across sources or
   accounts.
7. Download is a POST over the selection.

## Consequences

- Selecting and completing are distinct controls.
- Moves across accounts or servers preserve the source on failure.

## References

- RFC 4918 9.9 (`MOVE`), RFC 9110 13.1.1 (`If-Match`), 13.1.2
  (`If-None-Match`)
- ADR 34, ADR 88
- `templates/partials/tasks/row.html`, `templates/pages/tasks/list.html`,
  `templates/assets/helpers.js` (bulk form gating)
- `pim/web/pooled.go` (`Selected`), `pim/web/act.go` (`Run`),
  `pim/calendar/web/tasks.go` (`move`), `pim/calendar/web/copy.go`
