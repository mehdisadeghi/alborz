# ADR 64: PIM rail and mirror at scale

Status: accepted. Amends ADR 35 (the rail) and ADR 53 (mirror reads).

## Context

With many accounts, each holding tens of calendars and thousands of
objects (`make rig-scale`), every PIM page drew rows for every
collection of every account, and list pages spent their time reading
the mirror: each read unsealed and decoded every object from disk,
parsed every object to filter it, and queried collections without a
per-account bound.

## Decision

1. With more than one account, the calendar, task and contact rails
   MUST draw the All link and one `details` heading per account. The
   chevron opens or closes the account's collections in place; the
   account name links to that account's scope. The page already loads
   collection metadata for every account, and draws the rows from it
   without reading collection objects. Nothing is filled by script,
   so the page works without JavaScript (ADR 13).
2. The mail rail keeps `details` per account; its rows are the
   subscribed mailboxes, bounded by the reader.
3. Collection pickers stay a native `select` with an `optgroup` per
   account (`collection-picker`); type-ahead is the search.
4. The mirror MUST keep each read collection's decoded objects in
   memory and drop them on any change to that collection (sync, own
   write, drop).
5. Held objects per kind (calendars, address books) MUST NOT exceed
   `hotMaxBytes` = 128 MiB across all accounts, counted as the objects'
   headers plus name, ETag and data bytes. Past it, the least recently
   read collections are released from memory whole; disk is untouched
   and the next read reopens them.
6. A calendar-query filter MUST skip parsing an object that lacks the
   `BEGIN:` line of a component the filter requires, matched unfolded
   and case-insensitively (RFC 5545 3.1).
7. Queries across collections MUST run at most `MaxConcurrency` (4)
   collections of one account at a time; different accounts run
   concurrently.

## Consequences

- When a page reads more than the budget, each read evicts a
  collection needed next, and every read reopens from disk.
- The rail's HTML grows with the total number of collections, even if
  their account headings are closed. Collection objects are still read
  only as the list needs them.
- An account with hundreds of collections still draws all of them in
  its own scope.
- Remaining per-page cost is ADR 53's: every object of a list passes
  through XML and is parsed, since PIM lists are sorted and cut in
  alborz.

## References

- RFC 5545 3.1, RFC 4791 7.8
- ADR 13, ADR 35, ADR 53
- `templates/partials/rail.html` (`scope-rail`),
  `templates/partials/form.html` (`collection-picker`)
- `pim/dav/mirror.go` (`hotMaxBytes`, `objects`, `hold`, `change`,
  `sizeOf`), `pim/dav/copy.go` (`beginsAll`, `holdsLine`),
  `pim/dav/each.go` (`MaxConcurrency`, `Each`, `Across`)
