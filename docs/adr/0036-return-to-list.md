# ADR 36: Return to the originating list

Status: accepted, amended by ADR 37 (the page's scope is read from
`from`) and ADR 61 (neighbours carry `from`).

## Context

An object's page (message, event, task, contact) names its owning
account in `account`, since that account's server holds it. The list
it was opened from also reads its scope from `account`, or from its
absence for the merged view. Without more, an object opened from the
merged view cannot return there.

## Decision

1. Every row link MUST carry `from`: the list's own path and query
   (scope, narrowing, sort, page).
2. `Context.From` reads `from` under the same rule as `next`: a local
   path on this site, otherwise empty.
3. Where `from` is present, it is the object's way back: the back
   link, the `next` of every action that settles the object, the
   Cancel of its edit form. Neighbour links MUST pass it on.
4. Where `from` is absent (e.g. a bookmark), the way back is the list
   the object's own parameters name.
5. `from` does not replace the list parameters the neighbours read;
   it only names where to return.
6. Pages that belong to one account by nature (rules, forwarding,
   auto-reply, account settings) carry no `from`.

## Consequences

- Object URLs gain one parameter.
- Edit forms post to their own address and keep `from`; list forms
  send `next`.

## References

- ADR 1, ADR 37
- `web/accounts.go` (`Context.From`, `localPath`, `NextOr`),
  `web/renderer.go` (`GlobalData.From`, `Own`), `mail/web/template.go`
  (`from` template func), `mail/web/navigation.go`,
  `pim/web/neighbours.go` (`RowParams`), `pim/web/made.go`
