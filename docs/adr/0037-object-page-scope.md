# ADR 37: Scope on object pages

Status: accepted. Amends ADR 1 and ADR 36.

## Context

ADR 1 makes the URL the scope: `account` names one account; its
absence names the merged view. An object's page also names its owner
in `account`. Opening an object from the merged view therefore moved
the account switcher, nav and rail to the owner's account without the
reader choosing it.

## Decision

1. Scopes nest: "All accounts" contains every account. An object is
   inside a scope when the scope is "All accounts" or its own account.
2. Choosing sets the scope; following keeps it. Selecting a rail row
   or a switcher entry is choosing. Opening an object from a list or
   following its neighbour links is following.
3. On an object's page the scope (switcher, nav, rail) MUST be the
   originating list's, read from `from` (ADR 36). The owner stays in
   `account` for every link and form that acts on the object.
4. On an object's page, choosing its own account or "All accounts"
   MUST stay on the object in that scope; choosing another account
   goes to that account's list.
5. On a list, a choice goes to the chosen scope's list.

## Consequences

- A page carries two accounts, the scope and the owner. They differ
  only for an object opened from the merged view.

## References

- ADR 1, ADR 36
- `web/renderer.go` (`GlobalData.Scope`, `Merged`, `Item`, `Own`),
  `templates/partials/nav.html` (account switcher)
