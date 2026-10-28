# ADR 46: Merged mailbox paging

Status: accepted.

## Context

A merged mailbox (one role across accounts) cannot know which account's
rows fall on page n without merging all rows above it. Fetching the top
(n+1) x page-size whole rows per account, per page, made deep pages
exceed the listing time budget.

## Decision

1. The merge reads in two passes:
   1. Each account FETCHes ENVELOPE, FLAGS, UID, RFC822.SIZE and
      INTERNALDATE over the window; the results are merged and the page
      is cut.
   2. BODYSTRUCTURE and the list headers are fetched only for the
      page's UIDs, only from accounts that have rows on it.
2. Each account holds one window per merged view, sized to the pages up
   to the one asked rounded up to a power of two, or the size already
   held if larger. Walking n pages refetches the window log2(n) times;
   going back costs nothing.
3. Whole rows fetched by the second pass are written into the held
   window.
4. A one-page window is fetched whole in one pass.
5. The held window is invalidated like any merged listing: on any
   change reported by the watcher or by a write.
6. A row the second pass cannot fetch is left off the page and its
   account reported as not answering.

## Consequences

- Page 122 at 50 rows costs one key-field FETCH of 128 pages per
  account plus one whole FETCH of at most 50 rows; pages up to 128
  reuse the window.
- The default merged order compares the Date header (INTERNALDATE when
  absent). Ordering by INTERNALDATE alone would shrink pass one but
  change the order; not done.
- Search across mailboxes cuts its page from a merge the same way but
  keeps a single pass of whole rows.

## References

- RFC 9051 section 6.4.5 (FETCH)
- ADR 6, ADR 34, ADR 61
- `mail/listing.go` (`mergedWindow`, `ListAsk.held`, `wholePage`,
  `cutPage`, `Merged`, `unifiedLess`, `searchWindow`), `mail/imap.go`
  (`mergeFetchOptions`, `listFetchOptions`, `wholeRows`)
