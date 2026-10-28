# ADR 1: Account scope

Status: accepted, amended by ADR 37 (an object's page keeps the scope
of the list it was opened from) and ADR 80 (choosing which accounts
feed a merged mail folder).

## Context

Several accounts can be signed in to one browser session. Page state,
standing preferences and per-browser state must not be mixed.

## Decision

1. The URL MUST carry identity and the question. Identity is the
   `account` query parameter and the object's path; the question is
   search, sort, page, date and view.
2. Stored state MUST hold standing preferences only. Where it and the
   URL disagree, the URL wins.
3. No cookie pins a current account or a merged mode. Without
   `account` and with more than one account signed in, a page is the
   merged view over every account of the session. An `account` not
   signed in to the session is refused with `account.notsignedin`.
4. The set of signed-in accounts is per browser session.
5. Which calendars, task lists, journals, address books and role
   mailboxes feed the merged view is stored per account
   (`caldav.settings`, `carddav.settings`, `base.settings`, ADR 80) and
   unioned at render time. No account is primary.
6. An account that fails MUST NOT fail a merged page: it contributes
   nothing, is logged, and is named in a notice.
7. Settings are per account: a merged GET of a settings page redirects
   to name an account.

## Consequences

- A URL is shareable and means the same in every browser.
- Fan-out cost is bounded by the accounts signed in to the session.

## References

- ADR 37, ADR 80
- `web/middleware.go` (account resolution, `ctx.Unified`),
  `web/accounts.go` (`Scoped`, `AccountPath`), `web/session.go`
  (`Session.Accounts`)
- `pim/pooled.go` (`Visible`), `pim/web/pooled.go` (`Pooled`),
  `pim/calendar/settings.go`, `pim/contacts/settings.go`,
  `mail/settings.go` (`Unmerged`)
- `mail/listing.go` (`mergedWindow`, `gather`)
