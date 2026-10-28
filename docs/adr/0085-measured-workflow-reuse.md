# ADR 85: Measured workflow reuse

Status: accepted.

## Context

Repeated page frames, form decoders, list state and resource projections
across mail, PIM, filters and settings create maintenance drift. Sharing
code helps only when the shared boundary preserves each feature's exact
validation, storage and error semantics and reduces net production code.

## Decision

1. Shared workflow abstractions MUST preserve the exact HTTP status,
   notice, optimistic-concurrency and error semantics of their callers,
   and MUST reduce the strict production Go line count (tracked `*.go`
   files excluding `_test.go`, `internal/alborztest`, blank lines and
   comments).
2. Common page chrome MUST use `web.PageData[R]` and shared template
   partials while keeping feature rail types concrete at the page
   boundary (ADR 81).
3. List query parameters, parsed search queries, view filters, sort
   direction, pagination and `from` return-path assembly MUST use the
   shared contract in `web` (`web/liststate.go`, `web/filters_shown.go`),
   while section-specific projections stay in the feature package.
4. Form decoding and optimistic `shown`-snapshot checks MUST preserve the
   distinction between absent fields (untouched) and present empty
   fields (cleared).
5. Upload and import handlers MUST remain separated where their resource
   contracts differ (settings archives, PIM `.ics`/`.vcf`/`.zip` imports,
   mbox streams and Sieve scripts).
6. Protocol logic stays in the pinned `alborzmail` libraries (ADR 57);
   application workflow helpers stay in typed Go packages without
   introducing generic cross-protocol repositories.

## Consequences

- Shared helpers are accepted only when they shrink production code
  without weakening type safety or protocol error handling.
- Mail and PIM lists share one URL parameter and pagination contract
  while keeping their distinct storage and search backends.

## References

- ADR 55, ADR 57, ADR 61, ADR 66, ADR 81
- `web/page.go`, `web/liststate.go`, `web/filters_shown.go`,
  `web/form.go`, `pim/web/pages.go`, `pim/calendar/web/list.go`,
  `pim/calendar/web/edit.go`
