# ADR 62: Subscribed calendars

Status: accepted. Amends ADR 24 and ADR 54 (subscribed calendars are
kept in the store, not cached).

## Context

A subscribed iCalendar feed was stored in account settings and drawn
from a separate cache, so every page special-cased it and CalDAV
clients never saw it. The list of subscriptions must still travel
between alborz instances with the account's settings (ADR 52).

## Decision

### List and calendar

1. The account setting `caldav.subscriptions` lists each subscription
   (URL, name, colour, time the name or colour changed). It is the
   only part that travels between instances.
2. Each entry MUST be a calendar in alborz's own store (ADR 24), owned
   by the account, with ID `feed-` plus a keyed hash of the URL, so
   concurrent creation yields one calendar. The collection holds the
   feed state: URL, ETag, Last-Modified, last fetch, interval, and
   when its name or colour changed.
3. Pages and DAV clients MUST treat it as any calendar: rail, month,
   day, event page, export, rename, recolour, share, delete.

### Filling

4. Only the feed writes the objects. CalDAV clients get a read-only
   privilege set, and any object write, including the owner's, is
   refused with 403.
5. A fetch MUST be split into one object per UID, named as an import
   names it, and all objects replaced in one transaction together
   with the validators. The ctag MUST change only when an object
   changed.
6. An event without DTSTAMP gets its LAST-MODIFIED, else CREATED, else
   the epoch; never the fetch time, which would change every ETag.
7. Fetches are conditional GETs (If-None-Match, If-Modified-Since,
   RFC 9110 13.1). Validators are sent only when every calendar of
   that URL holds the same copy. A URL is fetched once per round for
   all accounts that follow it.
8. The interval is the feed's REFRESH-INTERVAL (RFC 7986) or
   X-PUBLISHED-TTL, clamped to 15 min .. 24 h; 1 h when absent. A
   failed URL is retried no sooner than 15 min. Feeds are refreshed on
   their interval whether or not a page reads them, since CalDAV
   clients read them without alborz seeing.
9. At most 4 MiB of a response is read; a `text/html` response is
   refused as not a feed.
10. The collection is a plain calendar: no `CS:subscribed`, so clients
    list it as a read-only CalDAV calendar and do not fetch the feed
    themselves (DAVx5 shows it under CalDAV, not Webcal).
11. The feed URL may carry a secret token. It is shown only to the
    owner, on the calendar's page. DAV serves no `CS:source`, which no
    client acts on for a calendar, and a share's guest sees neither the
    URL nor that the calendar is a subscription; a copy the guest makes
    names the calendar's DAV address as its source. Not covered:
    `alborz.db` and the account's settings hold the URL, so anyone who
    reads those reads it.

### Reconciliation

Run whenever an account's calendars are opened:

12. An entry without a calendar here MUST be created and fetched
    while the request waits; a failed fetch leaves it empty until the
    next round.
13. Deleting the calendar here, from a page or a DAV client, MUST
    record the URL and time. The next reconciliation removes the entry
    unless it changed after the delete (a new subscription), then
    forgets the record.
14. A calendar whose entry is gone from the list MUST be deleted.
15. Renames and recolours are timestamped on both sides; the newer
    wins on both.

## Consequences

- Name, colour and list changes propagate only when a page opens the
  calendars on that instance.
- "Newer" uses the clock of the instance that made the change.
- A domain whose DAV server is another alborz lists that instance's
  copy beside this one's.
- Feed contents are in `alborz.db`, sealed as every stored calendar
  (ADR 70); `-cache=false` does not affect them.

## References

- RFC 5545 3.6.1, 3.8.7.2; RFC 7986 5.7; RFC 9110 13.1; RFC 4791
- ADR 24, ADR 52, ADR 54, ADR 70
- `pim/calendar/subscriptions.go` (`Subscription`, `feeds`,
  `refreshEvery`, `fetchFeed`, `stamp`, `reconcile`, `follows`),
  `pim/calendar/caldav.go` (`OpenWithFeeds`),
  `pim/calendar/settings.go` (`subscriptionsKey`)
- `pim/store/store.go` (`FeedRef`, `Fill`, `Feeds`, `Dropped`,
  `writable`), `pim/store/caldav.go` (`calendarOf`),
  `pim/dav/sharing.go` (`Provider.Feed`)
