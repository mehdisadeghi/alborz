# ADR 80: Choosing what feeds the merged folders

Status: accepted. Amends ADR 1 and ADR 35.

## Context

In a multi-account session, a merged mailbox (`/mailbox/<role>` without
`account`) unions that role across every signed-in account. Readers need
a way to exclude an account's role folder from the merged view, matching
the inclusion checkboxes that DAV collections have in the rail (ADR 35).

## Decision

1. Each account's mail settings (`base.settings`, ADR 52) hold
   `Unmerged`: the role names (`mail.Roles`) excluded from merged
   mailboxes for that account. Empty means all six roles are included.
2. Only the six standard roles can be excluded; a merged folder opened
   by name includes every account holding that folder.
3. A merged role folder reads only from accounts that feed it: listings
   and paging (ADR 46), whole-view selection, message neighbours, empty
   folder, rail unread counts, app badge and status bar unread count.
   Single-account folder views are unaffected.
4. A role excluded in every account renders an empty list, not 404.
5. An account whose settings cannot be read because its server is
   unreachable is reported as not answering (ADR 1 rule 6).
6. In the merged mail rail, each role row under an account renders
   ADR 35's member checkbox (`common.shown`). Submitting posts the roles
   shown (`shown`) and ticked (`merged`), updating only the roles on the
   form (`alborz.Untick`) and redrawing the rail and list in place.
7. DAV visibility settings (`CalendarFilter`/`VisibleCalendars`,
   `TaskFilter`/`VisibleTasks`, `JournalFilter`/`VisibleJournals`,
   `AddressBookFilter`/`VisibleAddressBooks`) follow the same
   `shown`/ticked partial-update rule (`pim.Choose`). Collections
   created, accepted or subscribed in alborz are added to the visible
   set (`Show`).
8. Concurrent updates to a settings entry on the same account serialize
   through `Login.Update` and re-read the stored entry inside the lock.
9. Rapid ticks on the same form use `hx-sync="this:replace"` so the
   latest state replaces an in-flight request.

## Consequences

- Excluding an account from a merged role skips its IMAP queries for
  that merged view.
- Mail and DAV rails share one member-checkbox interaction model.

## References

- ADR 1, ADR 35, ADR 46, ADR 52
- `store.go` (`Login.Update`, `Untick`), `mail/settings.go` (`Unmerged`,
  `Merges`, `ChooseMerged`), `pim/calendar/settings.go`,
  `pim/contacts/settings.go`, `pim/pooled.go` (`Choose`),
  `pim/web/pooled.go` (`HandleChoose`), `mail/web/mailbox.go`,
  `mail/web/sidebar.go`, `templates/partials/rail.html`
