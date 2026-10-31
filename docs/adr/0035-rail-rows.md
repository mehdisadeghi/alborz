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
   account's scope. A DAV rail MUST draw the heading for a single
   account too, and MUST NOT add an "All" row under it: the heading
   opens all of the account's collections and is current only while
   no single collection is open.
3. **Places** are links (`aria-current` when active). Clicking a
   collection or folder name opens that collection or folder; All
   accounts at the top of the rail opens the merged view.
4. **Members** are inclusion checkboxes (`common.shown`) placed at the
   inline end of each collection row (and each role mailbox row in the
   merged mail rail, ADR 80). Ticking updates the standing merged-view
   membership and redraws the list and rail in place without scrolling
   the rail. Ticking while viewing a single collection widens the view
   to the whole account.
5. Views, groups and categories are place links in their own rail
   branches. Active view or query filters display a removable filter
   chip (`filter-chips`) above the list. A collection or folder link
   MUST NOT carry a star view: choosing a collection leaves the view.
   The Starred branch is folded unless a star view is in force.
6. A branch (`details` account or folder heading) MUST draw its
   disclosure chevron at the row's inline end, after the count and
   before any member checkbox, at every width; depth is shown by the
   row's indent. The label stays a link; the rest of the `summary`
   toggles the branch without script.
7. Inside an account, a DAV rail MUST group the collections by where
   they are kept (ADR 28 rule 9), in the order `Sources` lists the
   places, each group under a heading that names the place even when
   there is only one. The heading is plain text, neither link nor
   toggle, on the rows' edge. The server marks each group's first row
   (`dav.Collection.Heads`); templates MUST NOT group by comparing rows.
8. Rows that merge every account (`web.MergedRows`: settings' lists,
   filters' scripts) MUST sit under All accounts in the same merged
   branch (`aggregate-folders`) as the other sections' merged views.

## Consequences

- Opening a single collection is a click on its name; changing what
  feeds the merged view is a tick on its checkbox.
- Mail, calendar, tasks, journal and contacts share the same rail row
  anatomy.
- A remote place's heading carries the server's host and may wrap to a
  second line in a narrow rail.

## References

- ADR 1, ADR 28, ADR 64, ADR 80, ADR 102
- `templates/partials/rail.html` (`rail-dav-account`, `place`),
  `templates/partials/crumb.html` (`filter-chips`),
  `templates/partials/filters.html`, `templates/assets/style.css`,
  `pim/pooled.go` (`Visible`), `pim/dav/collection.go` (`ByPlace`),
  `pim/web/pooled.go`, `filters/web/rail.go`, `mail/web/sidebar.go`
