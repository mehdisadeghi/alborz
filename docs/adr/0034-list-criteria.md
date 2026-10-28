# ADR 34: List criteria and cross-folder search

Status: accepted, amended by ADR 61 (list paging and held orders).
Amends ADR 3 and ADR 21.

## Context

A mail list is narrowed by a query and a view. The list, its count,
whole-view selection, export and neighbour navigation must all name
the same set of messages, and a search across many folders must not
hold one IMAP connection turn past its deadline.

## Decision

1. **One criteria.** `listCriteria(query, view)` MUST be the SEARCH
   criteria for a list: the view's criteria ANDed with the query's.
   The list, count, whole-view selection, export and neighbours all use
   it.
2. **Address terms are verified.** For a query with `from:`, `to:`,
   `cc:` or `bcc:` terms, `carried` MUST FETCH the ENVELOPE of every
   SEARCH result and drop those the envelope contradicts, before the
   page is cut. A full-text index may match addresses inside attached
   messages; the envelope comparison is name and address, as a header
   SEARCH compares them.
3. **One turn per folder.** `/search` MUST run one `DoIMAPWork` per
   folder, each bounded by the listing budget (`RoundTripTimeout`,
   10 s; `ScanTimeout`, 2 min, for a query that searches text). Other
   requests for the account are served between turns.
4. An account whose server fails (`UpstreamError`) midway contributes
   neither rows nor count and is reported as unreachable.
5. **Undo references.** A message reference is
   `account|mailbox|uid`, with a fourth field, the origin mailbox, on
   an undo of a move, since search results span folders.
6. **Query syntax.** A quoted phrase and a `-`-prefixed word are
   separate terms. `-in:` excludes a folder from the search and does
   not scope it.

## Consequences

- An address search costs one ENVELOPE FETCH over all its SEARCH
  results; the listing cache holds the resulting order.
- A search over many folders takes the sum of their turns; only the
  request itself bounds the total.

## References

- RFC 9051 6.4.4 (SEARCH), 6.4.5 (FETCH ENVELOPE), 7.5.2
- ADR 3, ADR 21
- `mail/views.go` (`listCriteria`), `mail/imap.go` (`carried`,
  `envelopeCarries`, `viewOrder`), `mail/listing.go` (`searchWindow`,
  `gather`, `searchFolders`), `mail/prefetch.go` (`listingBudget`),
  `mail/actions.go` (`Ref`, `ParseRefs`), `search/query.go`,
  `mail/imapsearch/criteria.go`
