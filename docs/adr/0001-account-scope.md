# ADR 1: Account scope

Status: accepted, amended by ADR 80 and ADR 102.

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
   merged view over every account of the session; a page of the person
   is in no scope. An `account` not
   signed in to the session is refused with `account.notsignedin`.
4. The set of signed-in accounts is per browser session.
5. Which calendars, task lists, journals, address books and role
   mailboxes feed the merged view is stored per account
   (`caldav.settings`, `carddav.settings`, `base.settings`, ADR 80) and
   unioned at render time. No account is primary.
6. An account that fails MUST NOT fail a merged page: it contributes
   nothing, is logged, and is named in a notice.
7. A settings form is per account: a merged GET of it redirects to
   name an account. Settings lists merge (ADR 102).
8. Pages of the person (General, Passkeys and add-on status pages) MUST
   use `web.Personal`: neither an account's scope nor the merged view.
   Their navigation MUST carry no account parameter and their rail
   opens no account group. Choosing an account on a settings or personal
   page opens that account's Account form.
9. Object links from a list MUST carry `from` (a local path on this
   site naming the originating list's path and query). On an object
   page, `account` names the owning account for actions on the object,
   while `from` sets the page's navigation scope (merged or account),
   the return link, and the `next` target of actions that leave the
   object. Neighbour links pass `from` on; when `from` is absent, the
   object's own account and collection are used.

## Consequences

- A URL is shareable and means the same in every browser.
- Fan-out cost is bounded by the accounts signed in to the session.
- Opening an object from a merged list keeps the merged navigation scope
  while addressing writes to the owning account.

## References

- ADR 80, ADR 102
- `web/middleware.go` (account resolution, `ctx.Unified`),
  `web/accounts.go` (`Scoped`, `Personal`, `InScope`, `AccountPath`,
  `Context.From`, `NextOr`), `web/renderer.go` (`GlobalData.Scope`,
  `From`, `Merged`, `Own`), `web/session.go` (`Session.Accounts`)
- `pim/pooled.go` (`Visible`), `pim/web/pooled.go` (`Pooled`),
  `pim/calendar/settings.go`, `pim/contacts/settings.go`,
  `mail/settings.go` (`Unmerged`)
- `mail/listing.go` (`mergedWindow`, `gather`)
