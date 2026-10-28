# ADR 2: Task completion filter

Status: accepted.

## Context

The task list shows open tasks by default and has `completed` and
`all` views. A CALDAV:prop-filter with a text-match matches only a
property that is defined (RFC 4791 9.7.2), and a calendar-query has no
OR between filters.

## Decision

1. The calendar-query for tasks MUST filter on component only
   (VCALENDAR/VTODO). It MUST NOT filter on `STATUS` or on a time range.
2. Completion is decided on the client: a VTODO is completed when its
   `STATUS` is `COMPLETED`. The view, star, priority and text search
   filters apply in the same pass.

## Consequences

- A VTODO without `STATUS` or without dates is listed as open; a
  negated `STATUS` text-match or a time-range filter would drop it.
- Every VTODO of a visible list is transferred. The query is answered
  from the local copy of the collection (ADR 53), so this costs no
  extra round trip.
- Revisit only if a server can express "absent OR not COMPLETED" in
  one query.

## References

- RFC 4791 7.8, 9.7.1, 9.7.2, 9.7.5; RFC 5545 3.8.1.11
- ADR 53
- `pim/calendar/caldav.go` (`TaskQuery`), `pim/calendar/web/tasks.go`
  (task list filter), `pim/dav/copy.go` (`QueryCalendarObjects`)
