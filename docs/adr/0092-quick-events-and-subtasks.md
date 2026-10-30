# ADR 92: Quick events and subtasks

Status: accepted.

## Context

Calendar views need a quick-add bar like the task list's so a reader can
type a short line (`Lunch tomorrow at noon`) to create an event, and
tasks need one-level parent/subtask relationships (`RELATED-TO`,
RFC 5545 3.8.4.5).

## Decision

### Quick events

1. Calendar quick-add selects its destination collection by the same
   rule as task quick-add: a single visible writable collection is used
   directly; otherwise a collection picker is shown. Persistently hidden
   collections (`VisibleCalendars`, ADR 80) are excluded; temporary list
   filters do not hide destinations.
2. The server parser extracts date, time or time range, and duration
   from anywhere in the title, supporting Gregorian ISO (`YYYY-MM-DD`)
   and dotted (`DD.MM.YYYY`) dates, localized `today`/`tomorrow`,
   English weekday and month names, `in N days`, `noon`/`midnight`,
   12-hour (`am`/`pm`) and 24-hour clocks, time ranges and `for N
   minutes/hours`, normalizing localized digits while preserving the
   remaining title text.
3. Date-only input creates an all-day event. Time-only input, weekdays
   and month-day dates without a year resolve to their next occurrence.
   Bare hours after `at` (`calendar.quick.at`) use 24-hour time. Timed
   events default to 1 hour; a trailing meridiem in a range applies to
   both clocks.
4. Conflicting dates or clocks, invalid calendar dates, recurrence
   phrases, and ambiguous or nonexistent daylight-saving local times
   MUST be refused rather than falling back to all-day.
5. While typing, a compact preview overlay queries the server parser and
   shows the interpreted time first; validation failures re-render the
   calendar with the draft, selected collection and link to the full
   event editor intact.

### Subtasks

6. A subtask is any `VTODO` linked to a parent by
   `RELATED-TO;RELTYPE=PARENT` (RFC 5545 3.8.4.5) within the same
   task collection.
7. The task edit form offers a Parent select listing the collection's
   tasks; an empty choice removes the parent. It MUST NOT offer the task
   itself or its descendants, and the server MUST refuse a parent
   outside that set. New tasks have no parent until edited.
8. Parent and subtask pages link to each other; the parent page lists
   every child, including completed ones. Marking a parent or subtask
   complete MUST NOT cascade to its relatives.

## Consequences

- Quick-add uses deterministic extraction with no external NLP or
  date-parsing dependency.
- Subtask relationships use standard iCalendar `RELATED-TO` and are
  preserved across CalDAV clients.

## References

- RFC 5545 3.8.4.5 (`RELATED-TO`)
- ADR 2, ADR 49, ADR 80, ADR 81
- `pim/calendar/web/quick.go`, `pim/calendar/web/subtasks.go`,
  `pim/calendar/subtasks.go`, `pim/calendar/caldav.go`,
  `templates/pages/tasks/task-edit.html`,
  `templates/partials/calendar/toolbar.html`
