# ADR 35: Rail rows: places and members

Status: accepted, amended by ADR 64 (account `<details>` groups in PIM
rails) and ADR 80 (member checkboxes in the merged mail rail).

## Context

A rail row can represent either a place ("open this view now") or a
standing membership choice ("include this in my merged view"). Combining
both into a single checkbox made the displayed list disagree with the
ticked set and left no way to open a single collection.

## Decision

1. Every rail row MUST be either a place link or a separate member
   control; one control MUST NOT act as both.
2. Account headings group an account's rows (`<details>` when multiple
   accounts are signed in, ADR 64); the account name links to that
   account's scope.
3. **Places** are links (`aria-current` when active). Clicking a
   collection or folder name opens that collection or folder; "All" at
   the top of the rail opens the merged view.
4. **Members** are inclusion checkboxes (`common.shown`) placed at the
   inline end of each collection row (and each role mailbox row in the
   merged mail rail, ADR 80). Ticking updates the standing merged-view
   membership and redraws the list and rail in place without scrolling
   the rail. Ticking while viewing a single collection widens the view
   to "All".
5. Views, groups and categories are place links in their own rail
   branches. Active view or query filters display a removable filter
   chip (`filter-chips`) above the list.

## Consequences

- Opening a single collection is a click on its name; changing what
  feeds the merged view is a tick on its checkbox.
- Mail, calendar, tasks, journal and contacts share the same rail row
  anatomy.

## References

- ADR 1, ADR 64, ADR 80
- `templates/partials/rail.html`, `templates/partials/crumb.html`
  (`filter-chips`), `pim/web/pooled.go`, `mail/web/sidebar.go`
