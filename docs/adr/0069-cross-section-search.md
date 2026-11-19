# ADR 69: Search across sections

Status: accepted, amended by ADR 75 (journal search section). Amends
ADR 21 and ADR 23.

## Context

Searching mail, events, tasks, journal entries and contacts from a
single header control requires a shared query grammar and fast
incremental results while typing, without issuing thousands of IMAP
`SEARCH` commands on every keystroke.

## Decision

### Shared grammar and searchers

1. Package `search` parses a query into bare words, quoted phrases,
   `key:value` pairs and `-` negations (`Keys()`, `Words()`), without
   assigning section-specific meaning to keys.
2. Each section registers `web.Searcher{Kind, Answers, Rows, Search,
   Held}` on `web.Server.Searchers`:
   - `Search` runs the full query (up to `searchLimit = 5` preview rows)
     and returns `web.Found{Rows, Total, All, Down}`;
   - `Held` (optional) answers immediately from in-memory caches without
     contacting upstream servers.
   `/search` (`core.search`) contains no section-specific code and
   renders sections in mount order with `Held` sections last (`events`,
   `tasks`, `journal`, `contacts`, `mail`).
3. A section is queried only if `Answers` includes every key in the
   query; otherwise its block lists unsupported keys under "Not
   searched:" (`search.unasked`).
4. Sections run concurrently. `view=` and `is:` star/colour filters are
   resolved consistently across sections (`web.StarFilter`); two colours
   in one query return 400.
5. Section implementations:
   - **mail** (`/mail/search`): `Held` searches cached listings
     (`listingCache.held`, excluding junk, trash and stale entries) over
     `From`, `To`, `Cc` and `Subject`; `Search` runs the full cross-folder
     IMAP search (ADR 21, ADR 34), superseding any older in-flight
     search for the same account (`alborz.ErrSuperseded`, 409);
   - **events** (`/calendar/search`): matches `SUMMARY`, `DESCRIPTION`
     and `LOCATION` across visible calendars, ordered by upcoming
     occurrences first (soonest first), then past occurrences (latest
     first);
   - **tasks**: matches `SUMMARY` and `DESCRIPTION`;
   - **journal**: matches `SUMMARY`, `DESCRIPTION` and `CATEGORIES`
     (ADR 75);
   - **contacts**: matches every `FN`, `N`, `NICKNAME`, `ORG` and
     `EMAIL` of the cards the address books in scope hold, read whole
     as the list reads them.

### Header search dialog

6. Every signed-in page renders a header search button that opens
   `dialog#search-dialog` (`commandfor`, `show-modal`,
   `closedby="any"`), bound to `/` and `Ctrl-K` / `⌘-K` (ADR 23).
7. Without JavaScript, submitting the dialog's GET form navigates to
   `/search`, which runs every section in full.
8. With JavaScript:
   - typing requests each section's block (`section=<kind>`,
     `HX-Target: search-dialog`) 250 ms after the last keystroke and
     aborts superseded requests;
   - sections with `Held` are requested with `held=1` at 250 ms and in
     full after 1 s of inactivity or on `Enter`;
   - arrow keys move focus between the input, result rows and "All"
     links;
   - the star/colour filter menu beside the input sets `view` for all
     sections.

### Case

9. Text alborz matches itself MUST be compared under `search.Fold`:
   Unicode full case folding (`ß` is `ss`, `ς` is `σ`), with the Turkish
   `İ` and `ı` read as `i` and the Arabic yeh (U+064A) and kaf (U+0643)
   read as the Persian ones (U+06CC, U+06A9). A letter with a combining
   mark stays another letter, and text is not normalized. This covers
   events, tasks, the journal, contacts, mail's `Held` rows and the
   envelopes of address terms (ADR 34).
10. Mail's `Search` is matched by the IMAP server and folds as that
    server folds it (ADR 21).

## Consequences

- Typing in the search box sends zero IMAP commands until the reader
  pauses for 1 s or presses `Enter`.
- Starting a new full mail search cancels the account's previous search
  at its next folder boundary.
- A `Held` row found under `search.Fold` leaves the dialog when the
  server's own search, which replaces it, folds less.
- A contacts search costs what the contacts list costs: every card of
  the books in scope, from the mirror (ADR 53) where one is kept.

## References

- ADR 13, ADR 21, ADR 23, ADR 34, ADR 53, ADR 54, ADR 75
- `search/query.go`, `search/fold.go`, `web/search.go`,
  `web/filters_shown.go` (`StarFilter`, `StarUnasked`),
  `mail/imapsearch/`, `mail/listing.go`
  (`Held`), `mail/cache.go` (`held`, `supersede`),
  `mail/web/searchall.go`, `pim/match.go`,
  `pim/calendar/web/search.go`, `pim/contacts/carddav.go` (`Matches`),
  `templates/partials/nav.html`
