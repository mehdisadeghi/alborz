# ADR 19: Pages saved for offline reading

Status: accepted, amended by ADR 25 (sessions with a passkey save no
pages), ADR 33 (slow vs. offline notice and search exemption), ADR 74
(push notifications) and ADR 90 (concurrent network and cache lookup).
Amends ADR 14.

## Context

Without saved pages, an installed app that loses connectivity or reaches
a restarting server cannot display pages the reader already had open.

## Decision

1. The service worker MUST try the network first for every HTML
   navigation and htmx GET request (`HX-Request: true`).
2. A saved page answers only when the network request fails or does not
   respond within 5 s (`NETWORK_WAIT`, ADR 33). Requests carrying a
   `query` parameter (searches) are never raced against the cache.
3. Only 2xx `text/html` GET responses are saved. Excluded: the sign-in
   page, download routes, redirects, `/events`, and any response with
   `Cache-Control: no-store`.
4. The cache key is the request URL plus `HX-Target` when present.
5. Saved pages MUST be bounded:
   - a POST to sign in, sign out, `/unlock/leave` or
     `/settings/sessions`, or any response with `Cache-Control:
     no-store`, deletes all saved pages;
   - pages older than 7 days (`PAGES_DAYS`) are not served;
   - at most 200 entries (`PAGES_MAX`) are kept, evicting oldest first.
6. The worker reports to each client whether its last response came from
   the network, failed (`offline`) or timed out (`slow`, ADR 33).
7. Uncached pages and non-GET writes fail normally when offline; writes
   are never queued.
8. Every HTML response carries the build's shell digest in
   `Alborz-Shell`, and each document carries `<meta
   name="alborz-shell">`. A response from a different shell triggers a
   full navigation reload (or is dropped if unasked) and is never
   swapped into a mismatched shell.

## Consequences

- An unlocked browser without passkeys retains up to 7 days (max 200) of
  recently viewed pages until sign-out.
- A deployment with new shell assets retires previously saved offline
  pages.

## References

- ADR 14, ADR 25, ADR 33, ADR 74, ADR 90
- `web/serviceworker.go`, `web/routes.go` (`/sw.js`),
  `web/context.go` (`Render`, `Alborz-Shell`),
  `templates/assets/helpers.js`
