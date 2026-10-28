# ADR 16: Files over WebDAV

Status: proposed.

## Context

Accounts with CalDAV and CardDAV often have a WebDAV file store too
(Nextcloud, ownCloud, Apache mod_dav, a NAS). The protocol surface is
small, but a file browser lets another server's bytes reach the
reader's browser on alborz's origin.

## Decision

1. Read only. A listing is PROPFIND Depth 1 for
   `getcontentlength`, `getcontenttype`, `getlastmodified`, `getetag`
   and `resourcetype`; a file is a GET. No PUT, MKCOL, DELETE, MOVE.
2. A file MUST NOT render on alborz's origin. A proxied GET MUST be
   answered with `Content-Disposition: attachment` and
   `Content-Type: application/octet-stream`, whatever the server sent.
   Inline display (images via `/proxy`, text or PDF in the sandboxed
   viewer) is a later decision.
3. Transfers stream upstream to browser without buffering; `Range`
   and `Content-Range` pass through. Each request is bounded by the
   DAV request timeout, and a timeout is logged by name.
4. The server is configured, not discovered (no SRV for WebDAV): an
   operator upstream per domain, `webdavs://host/path`, beside the
   caldav and carddav ones, using the account's DAV credentials. A
   domain without one has no Files section.
5. A folder uses the existing collection list presentation. Names
   carry `dir="auto"`; sizes use `humansize`; dates use the reader's
   calendar.
6. Quota, where offered, is read from `quota-used-bytes` and
   `quota-available-bytes` on the home collection and shown once with
   its interpretation.

## Consequences

- Writing, if added, needs `If-Match` on every PUT, respect for class 2
  locks, and a DELETE that states what it removes first.

## References

- RFC 4918 (WebDAV), RFC 4331 (quota), RFC 9110 section 14 (Range),
  RFC 6266 (Content-Disposition)
- ADR 15, ADR 22, ADR 28
- `server.go` (upstreams), `mail/web/routes.go` (`/proxy`),
  `mail/web/template.go` (`humansize`)
