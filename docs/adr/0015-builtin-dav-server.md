# ADR 15: Built-in DAV server

Status: superseded by ADR 24.

## Context

Accounts whose mail domain provides no CalDAV or CardDAV server need
built-in calendar, task and contact storage authenticated by the mail
account.

## Decision

1. Superseded by ADR 24: alborz authenticates DAV principals by IMAP
   login and serves CalDAV and CardDAV via `go-webdav`, but stores
   collections and objects in `alborz.db` rather than in IMAP mailboxes.

## Consequences

- Built-in DAV storage does not depend on the IMAP server supporting
  RFC 5464 `METADATA` or RFC 7162 `CONDSTORE`.

## References

- ADR 24
- `pim/store/`, `pim/web/owndav.go`
