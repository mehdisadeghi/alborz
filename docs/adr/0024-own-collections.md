# ADR 24: Own calendars, task lists and address books

Status: accepted, amended by ADR 26 (kept in `alborz.db`), ADR 28
(`here` source), ADR 32 (what a publication shows), ADR 62 (subscribed
calendars), ADR 70 (sealed) and ADR 72 (per-collection keys).
Supersedes ADR 15.

## Context

Without a DAV server of its own, an account whose domain has none has
no calendars, tasks or contacts, and accounts cannot share any.

## Decision

### Store

1. Collections (id, owner, kind, components, name, colour, ctag,
   publication secret), objects (the iCalendar or vCard as written,
   and its ETag) and shares are kept in `alborz.db` under the owner
   prefix `alborz.pim` (ADR 26, ADR 51), sealed (ADR 70).

### Server

2. The store is served over CalDAV (RFC 4791) and CardDAV (RFC 6352)
   at `/alborz/dav/`, with `/.well-known/caldav` and
   `/.well-known/carddav` redirecting there (RFC 6764). go-webdav's
   CalDAV and CardDAV handlers serve it over backends on the store; the
   root and principal name both home sets. A request body over 16 MiB
   (`maxObject`) is refused with 413.
3. DAV clients authenticate with HTTP Basic: the mail address and
   password, checked by an IMAP login. Verdicts, refusals included, are
   cached for 5 minutes (`davPasswordLife`).
4. alborz's own pages reach the store in-process as the session's
   account, through the same DAV client code as remote servers (ADR
   28's `here` source), so a share is visible at once.

### Sharing

5. The owner shares a collection with an address of a served domain,
   read or read-write, optionally until a last day. The invitee is
   mailed from the owner's account (`X-Alborz-Share` header) and
   accepts or declines where their collections are listed or beside
   that mail. A failed mail is a warning; the invitation stands.
6. An accepted share appears in the invitee's home set as
   `<id>~<owner>`; `~` cannot occur in a name.
7. A read share refuses writes to objects. Only the owner renames,
   recolours, deletes or shares. The owner revokes, the invitee leaves;
   after the last day the share gives no access.

### Publication

8. The owner MAY publish a calendar at `/pub/<secret>.ics`: the
   collection as one read-only iCalendar stream, no authentication
   (content per ADR 32).

## Consequences

- `alborz.db` holds data that exists nowhere else and needs backing
  up; two instances of one deployment must share it.
- Not provided: CalDAV scheduling (RFC 6638), free-busy, WebDAV sync
  (RFC 6578; clients fall back to ctag and ETags) and groups.

## References

- RFC 4791, RFC 6352, RFC 6764, RFC 5545
- ADR 26, ADR 28, ADR 32, ADR 51, ADR 62, ADR 70
- `pim/store/` (`store.go`: `Collection`, `Share`, `Name`;
  `handler.go`: `Handler`, `maxObject`; `paths.go`: `Prefix`,
  `aliasSep`; `public.go`: `PublicPrefix`),
  `pim/web/owndav.go` (`serveOwn`, `davPasswordLife`),
  `pim/web/sharing.go`
