# ADR 15: Built-in DAV server

Status: superseded by ADR 24.

Proposed shape for alborz serving CalDAV and CardDAV itself: the
principal is the IMAP account, authenticated by an IMAP login; each
collection is an IMAP mailbox typed with METADATA (RFC 5464), each
object one message holding one iCalendar or vCard, with ETags from the
message and `sync-collection` (RFC 6578) from CONDSTORE MODSEQ (RFC
7162); the protocol side is go-webdav's `caldav.Handler` and
`carddav.Handler`; `calendar-auto-schedule` (RFC 6638) is not
advertised until scheduling is complete. ADR 24 built the server with
the IMAP-login principal and go-webdav handlers but stores collections
in alborz's own database instead of IMAP mailboxes.
