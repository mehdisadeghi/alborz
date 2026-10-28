# ADR 33: Slow versus offline in the service worker

Status: accepted. Amends ADR 19.

## Context

ADR 19 serves a saved page when the network has not answered within
`NETWORK_WAIT` (5 s). A server that answers after 5 s is reachable, so
reporting "offline" in that case is false. Some requests, such as an
all-folder search on a server without a full-text index, routinely
take longer than 5 s; htmx allows 20 s.

## Decision

1. The race against a saved copy stays as ADR 19 defines it.
2. The worker MUST report which outcome occurred: a failed fetch is
   `offline`; a fetch aborted at `NETWORK_WAIT` is `slow`.
3. The one notice element, `#offline-notice`, carries both texts
   (`notice.offline`, `notice.slow`) and MUST show the one that
   applies.
4. A request whose URL has a `query` parameter is a search and MUST NOT
   be raced; it waits as long as the browser and htmx do. An htmx
   request for one passes the worker untouched: only the browser's own
   fetch carries the page's abort to the server (Chrome does not abort
   a fetch event's `request.signal`), and the server stops searching.
5. Other routes MAY be exempted only on a measurement showing they are
   slow by nature.
6. The outcome is kept per page (the request's client; a navigation's
   resulting client) and told only to that page: one tab's late answer
   says nothing of what another shows, and a page served from the
   network MUST NOT claim "Showing saved pages".

## Consequences

- On a dead connection a search fails as it would without a worker;
  no saved result is shown.
- Other slow pages (e.g. a first load of a very large folder) are
  still raced.

## References

- ADR 19
- `web/serviceworker.go` (`NETWORK_WAIT`, `page`, `tell`),
  `templates/assets/helpers.js` (offline notice),
  `templates/partials/notice.html`, `templates/layout.html`
  (htmx `timeout`), `locales/*.toml`
