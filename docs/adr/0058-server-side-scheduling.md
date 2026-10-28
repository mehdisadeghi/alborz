# ADR 58: Server-side scheduling suppresses iMIP

Status: accepted.

## Context

alborz schedules by iTIP over iMIP (RFC 5546, RFC 6047): saving an
event the account organises sends REQUEST to its attendees and CANCEL
to removed ones; deleting it sends CANCEL. A CalDAV server advertising
`calendar-auto-schedule` (RFC 6638 2) delivers the same messages
itself when the object is stored, so every attendee would receive each
message twice.

## Decision

1. The event MUST be stored before any scheduling message is sent.
2. Before sending REQUEST or CANCEL, alborz MUST send one OPTIONS
   request to the event's collection. If the DAV header lists
   `calendar-auto-schedule`, alborz MUST NOT send, and MUST NOT show
   the "sent" notice.
3. If the OPTIONS request fails, nothing is sent and the reader is told
   the invitations were not sent.
4. A REPLY to an invitation received by mail is always sent by mail to
   the organiser, regardless of the server.
5. alborz's own collections (ADR 24) do not advertise
   `calendar-auto-schedule`, so alborz sends their invitations.

## Consequences

- One OPTIONS per save of an event with other attendees, and per
  delete of an event the account organises.
- The servers settings page shows the capability per source.
- With a scheduling server, delivery and its failures are the server's
  and are not reported by alborz.

## References

- RFC 5546, RFC 6047, RFC 6638 2; RFC 4918 10.1
- ADR 24, ADR 57
- `pim/calendar/scheduling.go` (`ServerSchedules`),
  `pim/dav/advertised.go` (`Describe`), `pim/calendar/web/event.go`,
  `pim/calendar/web/invitation.go`, `pim/calendar/web/plugin.go`
