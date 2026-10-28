# ADR 6: Cache policy

Status: accepted, amended by ADR 45 (every folder is watched, not only
INBOX) and ADR 53 (DAV collections are synced into a mirror, not
cached as responses).

## Context

Revalidating a cache entry on the request puts a round trip (a ctag
PROPFIND, an IMAP STATUS) on every page view after the entry ages.

## Decision

1. A cached mail listing or mirrored DAV collection MUST be served at
   once, fresh or stale. A change made by another client shows at most
   one page view late.
2. A stale mail listing (older than 30 s) is checked after the response
   with one STATUS and refetched when the mailbox changed.
3. A mail listing the watcher marked changed MUST NOT be served; the
   request waits for a fresh one.
4. A listing unused for 24 h is dropped.
5. A mirrored collection older than its service's poll interval is
   synced after the response. A collection not yet held is synced
   first, waiting at most 4 s before the request goes to the server.
6. A scheduler round every 10 s syncs each collection read since start
   before it goes stale. Poll intervals are per service: 2 min for
   CalDAV, 10 min for CardDAV.
7. The mirror is kept on disk, sealed under a key derived from the
   login key, so a restart is warm and shutdown does not sign out.
8. Each DAV list page offers a forced refresh that syncs every held
   collection of every signed-in account now, waiting for a sync already under way
   and then starting another.

## Consequences

- The mirror files are `<kind>.db` in `$XDG_CACHE_HOME/alborz`
  (`os.UserCacheDir`). With `-cache=false` there is no mirror and
  every DAV read goes to the server.
- A changed login key refuses to open a mirror sealed under the old one
  (ADR 59).
- A collection loaded from disk is synced by the round only after its
  account's first request provides a transport.

## References

- RFC 3501 6.3.10 / RFC 9051 6.3.11 (STATUS), RFC 6578, getctag
- ADR 45, ADR 53, ADR 54, ADR 59
- `mail/cache.go` (`listingFreshFor`, `listingKeepFor`,
  `listingChanged`), `mail/watch.go`
- `pim/dav/mirror.go` (`ready`, `round`, `Refresh`, `OpenMirror`,
  `syncWait`, `tick`), `pim/dav/provider.go`,
  `pim/calendar/caldav.go`, `pim/contacts/carddav.go` (`Poll`),
  `cmd/alborz/main.go` (`-cache`, cache directory)
