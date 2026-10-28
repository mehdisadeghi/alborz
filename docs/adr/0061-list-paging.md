# ADR 61: List paging and neighbours

Status: accepted. Amends ADR 34 (neighbours read the list's criteria)
and ADR 36 (neighbours carry `from`).

## Context

Mail and PIM lists paged differently, a page past the end behaved
differently per list, and a sorted or searched mail view ran SORT or
SEARCH over the whole mailbox for every page and again for a message's
neighbours.

## Decision

### Pages

1. `alborz.Page` (number from zero, size) with `Within`, `Rows` and
   `Window` is the one page model. `web.Context.ListPage` reads `page`
   and `ipp` and MUST refuse a page number it cannot parse (400).
2. A page past the end MUST be served as the last page.
3. Every list MUST draw the `list-pager` partial. A list held whole
   uses `web.Paginate`; others build the pager with `web.NewPager`.

### Mail order

4. A sorted, searched or filtered mail view MUST hold its UIDs in the
   listing cache, keyed by mailbox, query, view, sort key, direction
   and the dates a relative query (`newer_than:`) resolves to; not by
   page or page size. Pages are cut from it by UID FETCH, and a
   message's neighbours and position are read from it.
5. A THREAD view holds its messages in reading order, each message's
   depth and each thread's start. Its pages count threads; a message
   opened from it steps through messages.
6. A held order MUST be dropped on any change to its mailbox: an own
   write, EXPUNGE or MOVE, the watcher marking the mailbox stale, or a
   STATUS that differs from the one the listing was served under.
7. The mailbox's own order (no query, view or sort) is not held: pages
   are sequence ranges, and a message's position is counted with
   ESEARCH (RFC 4731) where offered, else UID SEARCH ALL.
8. Held orders total at most 4 Mi words (16 MiB) and 16 Ki views.
   Past either, the least recently used are dropped down to three
   quarters of both. An order larger than the whole budget is not
   held and is recomputed per page.

### Neighbours

9. Every object page MUST draw `object-nav`: position in the list,
   and Previous and Next on `k` and `j`. A missing neighbour keeps its
   slot, disabled. Every pager steps on `g p` and `g n`.
10. A message opened from a merged mailbox or a search, named by
    `from`, steps through the merged rows.
11. A merged mailbox is a SPECIAL-USE role in every account, or a
    mailbox by name in the accounts that hold it. When every account
    answered and none holds it, the response is 404.
12. Forms (a filter rule, a Sieve script) have no neighbours; stepping
    away would discard input.

## Consequences

- One held order costs 4 bytes per message: 400 KB for 100,000.
- An order keyed by a relative date is recomputed when the day turns.
- Filter rules and scripts are paged; the calendar month view steps by
  month on the same keys.

## References

- RFC 9051 6.4.4, 6.4.9; RFC 5256 (SORT, THREAD); RFC 4731; RFC 6154
- ADR 34, ADR 36
- `profile.go` (`Page`), `web/filters_shown.go` (`ListPage`,
  `NewPager`, `Paginate`, `Neighbours`),
  `templates/partials/toolbar.html` (`list-pager`, `object-nav`)
- `mail/cache.go` (`orderView`, `listingCache.order`, `maxHeldWords`,
  `maxHeldOrders`, `stale`, `reorder`), `mail/listing.go`
  (`fetchRows`), `mail/imap.go` (`folderNeighbors`, `uidNeighbors`),
  `mail/web/navigation.go`, `mail/web/mailbox.go`
- `pim/web/neighbours.go` (`Around`)
