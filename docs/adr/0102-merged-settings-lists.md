# ADR 102: Merged settings lists

Status: accepted, amended by ADR 103 (header menus). Amends ADR 1 and
ADR 74.

## Context

A reader with several accounts compares their signatures, sessions,
servers and notifications side by side, as filters already are, while
each settings form still edits one account's data.

## Decision

1. Signatures, Servers, Sessions and Notifications are lists: a GET
   without `account` MUST list the rows of every signed-in account; a
   GET with `account` MUST list that account's rows only.
2. A list row MUST carry the account marker (`record-account`), and its
   actions and links MUST carry that account's `account` parameter and
   a `next` back to the list as shown.
3. Account, Password, Data, Encryption, a signature's own page and a
   server's own page are forms of one account. A merged GET of them
   MUST redirect to name an account (`web.Scoped`).
4. A new signature from the merged list MUST offer an account picker,
   as a new filter does; the save MUST return to the list it came
   from.
5. The settings rail MUST show the lists under All accounts, shaped as
   an account's rows (`web.MergedRows`).
6. On a settings page the header person menu's entry for an account
   MUST open that account's Account form, and its All accounts entry
   MUST open the first merged list, Signatures.

## Consequences

- A merged list costs one store read per account; the server list asks
  each account's features in turn.
- Notifications are no longer a page of the browser: with one account
  they are among that account's rows.

## References

- ADR 1, ADR 37, ADR 74, ADR 103
- `web/accounts.go` (`InScope`, `Scoped`), `web/profile.go`
  (`SettingsRail`, `MergedRows`), `templates/partials/rail.html`
  (`scope-rail`), `templates/partials/nav.html` (`person-menu`)
- `mail/web/account.go` (`handleSignatures`, `handleSignatureSave`),
  `settings/web/sessions.go`, `settings/web/servers.go`
  (`handleServers`), `settings/web/notifications.go`
- `templates/pages/settings/signatures.html`, `sessions.html`,
  `servers.html`, `notifications.html`, `signature-edit.html`
