# ADR 63: Copying events and tasks

Status: accepted, amended by ADR 75 (journal entries) and ADR 88 (bulk
copy, contacts and moving objects).

## Context

Readers need to copy events, tasks and journal entries from subscribed
feeds, read-only shares or writable collections into a writable
collection of any signed-in account.

## Decision

1. Event, task and journal entry pages MUST offer "Copy to" in the
   object menu (`calendar.event.copy`, `calendar.task.copy`,
   `calendar.entry.copy`) for any source collection, including read-only
   shares and subscribed feeds.
2. The destination page (`calendar/destination`) lists writable
   collections supporting the component across all signed-in accounts,
   grouped by account. Submitting without a valid destination returns
   422 (`form.destinationneeded`).
3. After copying, the response redirects to the new object with a notice
   naming the target collection and an Undo action that deletes the
   copy (ADR 4).
4. The copy MUST be a new iCalendar object with a fresh `UID` on every
   non-`VTIMEZONE` component (master and recurrence overrides alike) and
   a fresh `CREATED` and `DTSTAMP` (ADR 49), preserving `RRULE`, `RDATE`,
   `EXDATE`, `RSCALE`, `RECURRENCE-ID` overrides and `VTIMEZONE`.
5. The copy MUST strip `METHOD`, `ORGANIZER` and `ATTENDEE` so it is not
   a scheduling object (RFC 6638).
6. A copy whose `RSCALE` is not supported by the target collection MUST
   be refused before the `PUT` (RFC 7529 10, ADR 10).
7. Each copied component MUST record its origin in two `LINK` properties
   (RFC 9253) with `LINKREL=via`:
   - `VALUE=URI`: the feed URL for a subscribed calendar, else the
     source collection's DAV URL;
   - `VALUE=UID`: the original `UID`.
   `RELATED-TO` and `X-` properties MUST NOT be used for copy provenance.

## Consequences

- Any client supporting RFC 9253 `LINK` can inspect the copy's source.
- Bulk copy and object moves share the destination picker and validation
  (ADR 88).

## References

- RFC 5545, RFC 6638, RFC 7529 10, RFC 9253 (`LINK`, `LINKREL`)
- ADR 4, ADR 10, ADR 28, ADR 49, ADR 75, ADR 88
- `pim/calendar/web/copy.go`, `pim/calendar/caldav.go` (`AsCopy`,
  `link`), `pim/web/destinations.go`, `pim/dav/scales.go`
