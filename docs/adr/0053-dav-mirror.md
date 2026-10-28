# ADR 53: DAV mirror synced by sync-collection

Status: accepted, amended by ADR 64 (hot in-memory cache and bounded
concurrency) and ADR 73 (unreadable calendar objects). Amends ADR 6
(DAV reads) and ADR 20.

## Context

HTTP caching (RFC 9111) keys no request body and revalidates only GET,
so it cannot cache REPORT answers. RFC 6578 sync-collection reports
what changed since a token. go-webdav's server can answer CalDAV and
CardDAV reads over an in-process copy, as it does for alborz's own
collections (ADR 24).

## Decision

1. A mirror MUST keep, per remote collection an account reads, its
   sync token or ctag and every member's name, ETag and data.
2. The mirror sits above the source router and keys by request path,
   which names the server's host (ADR 28), under the principal: the
   account and the user name its servers know it by. A copy MUST NOT
   be served to another principal, nor when the principal is unknown.
3. It answers calendar-query, calendar-multiget, addressbook-query,
   addressbook-multiget and GET of a member by running go-webdav's
   server over the copy. PROPFIND and all other requests pass through.
4. Sync is RFC 6578 from the held token (empty the first time). A
   truncated answer (RFC 6578 3.6) continues from the returned token;
   a refused token (4xx) restarts from empty. Members sent without data
   are fetched by multiget.
5. If the server refuses the report (4xx, 501) or answers without a
   token, the collection is compared instead: PROPFIND `getctag`;
   nothing more while it is unchanged; else a Depth 1 PROPFIND of ETags
   and a multiget of what changed.
6. A 404 on the collection drops its copy and the account's collection
   list.
7. A held copy is served regardless of age once a sync of it was tried
   since start and, if older than the kind's poll (CalDAV 2 min,
   CardDAV 10 min), synced in the background (ADR 6). A collection not
   held, or held from before start, is synced first for at most 4 s
   (`syncWait`); then the held copy answers, or the request goes to the
   server. One sync is bounded by 30 s (`syncBudget`).
8. Every 10 s, 4 workers sync the collections read since start that
   would go stale before the next tick. A failed sync waits for the
   next due round. Force refresh syncs all held collections on the
   request.
9. Own writes (ADR 20): a successful PUT with a response ETag stores
   the sent body under it (RFC 4791 5.3.4, RFC 6352 6.3.2.3); without
   an ETag, or refused, the copy is marked stale. DELETE removes the
   member. MKCOL, MKCALENDAR and PROPPATCH drop the collection's copy.
10. Copies are kept across sign-out and server changes (ADR 59); a path
    on other servers names another host, so an old copy never answers
    for it.
11. Storage: one kv file per kind, `<cache-dir>/caldav.db` and
    `carddav.db`, never `alborz.db`. Values are sealed under a key
    derived from the login key; keys are keyed names (ADR 70):
    `dav.mirror/held/<account>/<name>/<collection>`,
    `dav.mirror/objects/<account>/<name>/<collection>/<object>`.
    Opened collections are held in memory up to 128 MiB per kind, least
    recently read evicted.
12. With `-cache=false` there is no mirror.

## Consequences

- A collection's first read costs one sync.
- Collection listing is memoised separately (5 min); counting members
  asks the server.

## References

- RFC 6578; RFC 4791 5.3.4, 7.8, 7.9; RFC 6352 6.3.2.3, 8.6, 8.7;
  RFC 9111; ADR 6, 20, 24, 28, 54, 59, 70
- `pim/dav/mirror.go` (`Mirror`, `Principal`, `tripper`, `ready`,
  `wrote`, `round`, `Refresh`), `pim/dav/sync.go` (`pull`,
  `syncCollection`, `compare`), `pim/dav/transport.go` (`httpClient`,
  `principal`), `pim/dav/provider.go` (`NewProvider`)
