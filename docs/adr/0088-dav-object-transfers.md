# ADR 88: Moving and copying DAV objects

Status: accepted. Amends ADR 3, ADR 20, ADR 63 and ADR 75.

## Context

Events, tasks, journal entries and contacts all need "Copy to" and "Move
to" actions across collections that may live on the same DAV server or
on different accounts and servers.

## Decision

1. Object menus for events, tasks, journal entries and contacts MUST
   offer both "Copy to" and "Move to". Read-only shares and subscribed
   calendars MAY be copy sources; only writable collections MAY be move
   sources or transfer destinations. Calendar destinations MUST support
   the object's component.
2. Selectable DAV lists MUST offer bulk "Copy to" across accounts,
   deduplicating selected references (including recurring event
   occurrences of the same resource). Partial failures report the copied
   count and retain only unfinished references for retry.
3. Copying creates a new `UID` and `DTSTAMP`/`REV`. Calendar copies
   strip `METHOD`, `ORGANIZER` and `ATTENDEE` and record their origin in
   `LINK` properties (ADR 63); contact copies retain the card with a new
   `UID` and `REV`.
4. Moving preserves the object's `UID` and content:
   - within the same account and DAV source, alborz MUST use conditional
     WebDAV `MOVE` (`Overwrite: F`, `If-Match`), falling back only when
     the server answers 405 or 501;
   - across sources or accounts, alborz MUST `PUT` to the destination
     with `If-None-Match: *` and then `DELETE` the source with
     `If-Match` using the strong ETag read from the source. Missing
     strong ETags refuse the move without touching the source.
5. For calendar objects with scheduling participants, the cross-server
   `PUT`/`DELETE` fallback MUST be refused if either server advertises
   `calendar-auto-schedule` or fails `OPTIONS` (ADR 58), so moving an
   event never sends invitations or cancellations. Native `MOVE` remains
   allowed.
6. If the source `DELETE` is explicitly refused and a fresh read
   confirms the source still exists, alborz rolls back the destination
   `PUT` conditioned on the created destination ETag. If the `DELETE`
   outcome is uncertain, both copies are kept and the refusal tells the
   reader to check both collections (`form.moveuncertain`).
7. Native `MOVE` invalidates both collections in the DAV mirror and
   increments their write counters (ADR 20, ADR 53).

## Consequences

- Cross-server moves never delete the source before confirming the
  destination write, and never delete the destination when source
  deletion is uncertain.
- Bulk task move (ADR 3) and single-object transfers share the same
  transfer rules.

## References

- RFC 4918 9.9 (`MOVE`), RFC 9110 13.1.1 (`If-Match`), 13.1.2
  (`If-None-Match`), RFC 6638
- ADR 3, ADR 20, ADR 53, ADR 58, ADR 63, ADR 75
- `pim/calendar/web/copy.go`, `pim/contacts/web/transfer.go`,
  `pim/web/destinations.go`, `pim/dav/mirror.go`
