# ADR 73: Unreadable calendar data

Status: accepted. Amends ADR 10 (rule 5) and ADR 53.

## Context

Calendar data comes from servers, files and other clients. A zone name
the host does not know, a malformed date or rule, or a file cut off
inside a component is expected input, not a defect of alborz. One such
object failed the store's and the mirror's calendar-query, and the
whole page with a 500.

## Decision

1. The binary MUST embed Go's zone database (`time/tzdata`), so a TZID
   the host lacks (a backward name such as US/Central on a host without
   tzdata-legacy) still resolves; the host's database comes first.
2. A TZID the zone database does not know MUST be read from the
   VTIMEZONE of the same object (RFC 5545 3.2.19, 3.6.5), its onsets
   expanded by go-recur into a TZif location, with a POSIX TZ footer
   where the rules in force have one. A TZID defined nowhere is read as
   floating: the wall clock is all it states, and UTC would claim an
   offset the source did not (go-ical fork).
3. A calendar-query MUST keep an object whose times or rule cannot be
   evaluated, and one that does not parse, rather than fail: it may
   match any range, and the client judges it (go-webdav fork `Filter`;
   `pim/store`, `pim/dav` backends).
4. A client MUST read a report past an object that does not parse; the
   object arrives with its bytes and no parsed data (go-webdav fork).
5. A calendar or task page MUST render what it read with 200. What it
   could not read, a whole calendar or one object, is left out, logged
   once per page with its cause, and named in one warning notice
   (`notice.calendarunread`). A series not expanded is still drawn at
   its own start (ADR 10 rule 5) and counted as not read.
6. A single object read (`calendar.GetObject`) that does not parse is
   `alborz.ErrUnreadable`, answered 502 with the `unreadable` message
   on the event and task pages, their edit, copy, move and invitation
   paths. Such an event can still be deleted. An export stops with an
   error rather than leave the object out.
7. 500 is reserved for defects of alborz.

## Consequences

- The binary grows by the zone database (about 450 kB).
- A VTIMEZONE without a POSIX form is spelled out to 2200; later times
  keep its last offset.
- A server built on the go-webdav fork returns an unreadable object to
  every query; clients must tolerate it.

## References

- RFC 4791 7.8, 9.9; RFC 5545 3.2.19, 3.3.5, 3.6.5; RFC 8536;
  ADR 10, 49, 53, 57
- `cmd/alborz/main.go`, `pim/calendar/caldav.go` (`QuerySites`,
  `Unread`, `Unparsed`, `Occurrences`), `pim/calendar/web/plugin.go`
  (`warnUnread`), `pim/store/caldav.go`, `pim/dav/copy.go`
  (`QueryCalendarObjects`), `pim/calendar/caldav.go` (`GetObject`),
  `server.go` (`ErrUnreadable`), `web/errors.go`; go-webdav fork
  `caldav.Filter` and client; go-ical fork `zone.go`
