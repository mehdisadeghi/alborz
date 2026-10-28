# ADR 57: Protocol code lives in libraries

Status: accepted.

## Context

Workarounds for library gaps inside alborz (hand-written WebDAV XML,
iCalendar and vCard handling, header parsing, debug redaction,
iTIP rules) are tested only through alborz and duplicate protocol
logic an application should not own.

## Decision

1. A protocol gap in a library MUST be fixed in that library, and
   alborz MUST use what the library then offers.
2. Forks live under the `alborzmail` organisation and are pinned by a
   `replace` in `go.mod`, each with a comment stating what it carries
   and which upstream PRs it holds. Each change SHOULD be offered
   upstream where upstream would accept it.
3. A protocol with no suitable Go library gets its own `alborzmail`
   module rather than a fork.

Current libraries:

| Library | Carries |
|---|---|
| go-webdav (fork) | collection client (MKCOL, PROPPATCH, listing), RFC 6578 sync client, ETags as sent, exported HTTP errors, RFC 4791 9.9 time ranges for recurrences, RSCALE (RFC 7529 `supported-rscale-set`), server PROPPATCH and dead properties, an object that does not read kept in queries and reports |
| go-ical (fork) | VTIMEZONE generation and reading, TZID lookup (RFC 5545 3.2.19), alarm triggers, splitting by UID, dropping repeated zones, recurrence via go-recur (RSCALE), strict decoding |
| go-vcard (fork) | REV with offset, all CATEGORIES lines, MEMBER, BDAY, escaping |
| go-msgauth (fork) | authres parser rework, `authres.ParseARC` |
| go-imap (fork) | ID fields, message/global BODYSTRUCTURE, truncated-body error, GETMETADATA option order (RFC 5464), LOGIN and AUTHENTICATE redacted in debug output |
| go-mbox (fork) | reversible mboxrd quoting |
| go-itip (own) | RFC 5546 REQUEST, REPLY, CANCEL and applying them, tested against the RFC's examples |
| go-recur (own) | RFC 5545 recurrence with RFC 7529 RSCALE |

## Consequences

- A protocol fix is two changes: the library and the pin.
- Forks MUST be kept rebased on upstream.
- What remains in alborz is application policy: when to send, what to
  show, what to keep.

## References

- RFC 4791, RFC 5464, RFC 5545, RFC 5546, RFC 6350, RFC 6578, RFC 7529
- ADR 49, ADR 58
- `go.mod` (`replace` directives and comments), `local/forks/`
