# ADR 81: Shared calendar object editing

Status: accepted.

## Context

Events, tasks and journal entries all open or create a CalDAV object,
resolve a writable collection, stamp and write the component with
optimistic concurrency, and redirect to its page. Duplicating that
lifecycle across three handlers caused error and concurrency handling to
drift.

## Decision

1. `pim/calendar/web/object.go` MUST own opening and translating a
   shared calendar object, and `pim/calendar/web/edit.go` MUST own the
   shared conditional-write lifecycle.
2. Each feature handler MUST supply its component selector, validation,
   scheduling or status rules, and refusal rendering; no cross-protocol
   repository abstraction is introduced.
3. `Group.Page` registers a handler that serves both `GET` and `POST` on
   the same route without altering route names or middleware.
4. `pim/calendar/web/list.go` MUST own occurrence expansion, unreadable
   collection tracking, and the typed query pipeline shared by calendar,
   task and journal lists, while each view keeps its own filtering and
   row projection.
5. `web.PageData[R]` MUST provide the typed render frame for pages with
   a concrete feature rail `R`. `templates/layout.html` owns the outer
   document, navigation, status bar and swap boundaries; page templates
   define only `rail`, `before-main`, `main-class`, `main` and `scripts`.

## Consequences

- Event, task and journal editors share one conditional-write and error
  path while keeping feature-specific validation in leaf handlers.
- Concrete rail types on `web.PageData[R]` prevent passing the wrong
  section's rail model to a template.

## References

- ADR 20, ADR 49, ADR 55, ADR 66, ADR 73, ADR 75
- `pim/calendar/web/object.go`, `pim/calendar/web/edit.go`,
  `pim/calendar/web/list.go`, `web/page.go` (`PageData`),
  `web/router.go` (`Group.Page`), `templates/layout.html`
