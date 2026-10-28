# ADR 54: Caches and the process schedule

Status: accepted, amended by ADR 62 (subscribed calendars are stored in
`alborz.db`, not cached) and ADR 79 (weather forecast caches). Builds on
ADR 6, ADR 18, ADR 20 and ADR 53.

## Context

Each cache holds data from a distinct upstream protocol and is
invalidated by signals specific to that source, while periodic
background tasks (DAV mirror sync, subscribed calendar feeds, event
reminders) need a single server-owned scheduler.

## Decision

### Caches

1. Each cache is invalidated by its own upstream signal:

   | Cache | Holds | Invalidated by | Lifetime |
   |---|---|---|---|
   | Mail listings (`mail/cache.go`) | mailbox view order and rows per account | IDLE/NOTIFY, changed `STATUS`, own writes (ADR 18) | memory; fresh 30 s, served stale, dropped after 24 h unused |
   | Message bodies (`mail/bodies.go`) | fetched text parts by `UIDVALIDITY` and UID | `UIDVALIDITY` change, expunge, flag writes | memory; 100 per account, 64 MiB total, parts <= 512 KiB |
   | Memos (`internal/memo`) | folder counts, unified roles, DAV collection list | age or own write | memory; 30 s to 5 min |
   | WKD keys (`mail/wkd.go`) | sender OpenPGP key or absence | age | memory; 24 h |
   | DAV mirror (`pim/dav`) | synced remote collections (ADR 53) | sync token or ctag change, own writes | disk (`caldav.db`, `carddav.db`) + 128 MiB hot memory per kind |

2. Memory caches start cold and warm on sign-in (ADR 7); the on-disk DAV
   mirror starts warm.
3. On-disk caches live in `os.UserCacheDir()/alborz` (mode `0700`), with
   keyed names and sealed values (ADR 70, ADR 72). A cache file whose
   `alborz.keycheck` fails to open refuses startup (ADR 59).
4. `-cache=false` disables the cache directory, the DAV mirror and
   memory cache lifetimes (`Options.CacheFor = 0`). Weather upstream
   caches remain active to honour third-party rate limits (ADR 79).

### Schedule

5. `Server.Schedule` (`schedule.go`) runs periodic background jobs on
   dedicated goroutines, skipping overlapping ticks and stopping cleanly
   on server close:
   - DAV mirror sync every 10 s (ADR 53);
   - subscribed calendar feeds every 1 min (ADR 62);
   - calendar event reminders every 1 min (ADR 74).

## Consequences

- Deleting the cache directory causes a cold start without losing
  authoritative data.

## References

- RFC 7986 5.7, RFC 9110 13.1
- ADR 6, ADR 7, ADR 18, ADR 19, ADR 20, ADR 26, ADR 53, ADR 59, ADR 62,
  ADR 74, ADR 79
- `schedule.go`, `server.go`, `mail/cache.go`, `mail/bodies.go`,
  `mail/wkd.go`, `internal/memo/memo.go`, `pim/dav/mirror.go`,
  `pim/calendar/subscriptions.go`, `pim/calendar/reminders.go`
