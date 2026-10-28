# ADR 32: Published calendar content

Status: accepted. Amends ADR 24 (defines what the published stream
holds).

## Context

A published calendar is served at `/pub/<secret>.ics` to anyone who
holds the address, without authentication. It MUST NOT carry what the
calendar's writers did not make public.

## Decision

### Published feed

1. An object with any component whose CLASS is not PUBLIC (RFC 5545
   3.8.1.3; absent means PUBLIC) MUST be omitted whole, since a
   recurrence override of a private series reveals the series. It is
   omitted, not replaced by a busy placeholder.
2. ATTENDEE and ORGANIZER MUST be removed from every component.
3. VALARM subcomponents MUST be removed; a subscriber's client would
   otherwise trigger the writer's alarms.
4. All other properties are served as stored.
5. The feed's ETag is the collection's ctag; `If-None-Match` with it
   answers 304.

### ETag on PUT

6. Per RFC 4791 5.3.4 a PUT response MAY carry a strong ETag only when
   the stored octets equal the octets sent. A PUT MUST answer with an
   ETag only in that case.
7. The store keeps an object's octets as sent (`Raw` from the
   go-webdav fork), and GET and REPORT answer those octets, so every
   PUT answers with an ETag and ADR 20's cache patch applies to any
   client's write.

## Consequences

- The published feed is not a copy of the calendar; export is.
- The calendar's page offers the address under `webcal:` too, which a phone hands to its calendar app.
- A private event is invisible to subscribers, with no marker.
- A write to a private event changes the ctag, so subscribers refetch
  an unchanged feed once.

## References

- RFC 5545 3.8.1.3, 3.8.4.1, 3.8.4.3, 3.6.6; RFC 4791 5.3.4;
  RFC 9110 8.8.3, 13.1.2
- ADR 20, ADR 24
- `pim/store/public.go` (`ServePublic`, `guarded`, `anonymous`,
  `feed`), `pim/store/caldav.go` (`PutCalendarObject`,
  `calendarObject`), `pim/store/paths.go` (`write`)
