# ADR 49: Full calendar-data in CalDAV reads

Status: accepted, amended by ADR 75 (`VJOURNAL` component set and
timestamp rules).

## Context

A CalDAV `REPORT` that restricts `calendar-data` to named properties
(RFC 4791 9.6) causes servers that honour the restriction to strip all
unnamed properties. Furthermore, `VTODO` components may carry `DTSTART`
without `DUE`, and `DTSTAMP` tracks last revision (RFC 5545 3.8.7.2)
while `CREATED` tracks store creation (3.8.7.1).

## Decision

1. Any CalDAV query whose result is displayed or edited MUST request the
   full component (`AllProps` and `AllComps` on `VEVENT`, `VTODO` or
   `VJOURNAL`).
2. Queries that only locate an object by UID or read fixed contact
   fields (`FN`, `EMAIL`) MAY request specific properties.
3. Property fallbacks on read:
   - added date: `CREATED` only (empty when absent, never `DTSTAMP`);
   - task due date: `DUE`, else `DTSTART`;
   - alarm `TRIGGER;RELATED=END`: effective end (`DTEND`, `DUE`, or
     start plus `DURATION`).
4. Creating or copying a component MUST set `CREATED`; editing MUST
   preserve `CREATED` and update both `DTSTAMP` and `LAST-MODIFIED`.
5. Missing collection properties use standard fallbacks: absent
   `current-user-privilege-set` means writable; absent
   `supported-calendar-component-set` means events only (not tasks or
   journal entries, ADR 75).
6. Recurrence expansion is requested from the server (RFC 4791 9.6.5)
   and expanded locally by `go-recur` when the server returns unexpanded
   rules.

## Consequences

- Lists, pages and editors have every component property available
  without per-field query changes.

## References

- RFC 4791 9.6, 9.6.5; RFC 5545 3.6.1-3.6.3, 3.8.6.3, 3.8.7.1-3.8.7.3;
  RFC 3744 5.4
- ADR 57, ADR 75
- `pim/calendar/caldav.go`, `pim/calendar/web/event.go`,
  `pim/calendar/web/task.go`, `pim/calendar/web/tasks.go`
