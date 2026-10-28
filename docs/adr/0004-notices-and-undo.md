# ADR 4: Notices and undo

Status: accepted, amended by ADR 41 (a notice is a `<dialog>` over the
page, dismissed without script), ADR 42 (every action sets a notice or
states that it needs none) and ADR 52 (the profile-conflict notice
carries two actions).

## Context

The result of an action is shown on the page the request redirects
to. Some actions have an exact inverse; most do not.

## Decision

1. A notice has a kind (`done`, `warning`, `failed`), a text, and
   actions. An action is a link when it navigates and a POST form when
   it changes state.
2. An action notice carries at most one action (Undo or "Add another");
   a profile-conflict notice carries two (ADR 52).
3. Undo MUST be offered only where the handler knows the exact inverse
   and carries it in the form fields, marked `undo=1`:
   - a message move: moved back by the UIDs the server returned in
     `COPYUID` (RFC 4315);
   - a flag add or remove: the opposite `STORE`;
   - a star change that takes a row out of its view;
   - a task marked completed or reopened;
   - a copied event, task, journal entry or contact: deleting the copy
     (ADR 63, ADR 88).
4. A `STORE` that replaces flags, a delete, an empty and a send MUST NOT
   offer undo. Changing only `\Seen` sets no notice.
5. The answer to an undo MUST NOT offer a further undo.
6. An undo move whose UIDs the server no longer holds moves nothing and
   answers with a `failed` notice.
7. Deleting a fully selected page of a mailbox that holds more offers
   the rest by count as a link to the empty-mailbox page, which states
   the count again before its POST. Deleting everything is never the
   default.

## Consequences

- Without `UIDPLUS` (RFC 4315, part of IMAP4rev2) a move returns no
  `COPYUID` and offers no undo.

## References

- RFC 4315 (`COPYUID`), RFC 9051 6.4.6 (`STORE`), 6.4.8 (`MOVE`)
- ADR 34, ADR 41, ADR 42, ADR 52, ADR 63, ADR 88
- `web/notices.go` (`Notice`, `NoticeAction`, `Undo`),
  `templates/partials/notice.html`, `web/filters_shown.go` (`StarNotice`)
- `mail/web/mailbox.go`, `mail/actions.go` (`Move`),
  `pim/calendar/web/tasks.go`, `pim/calendar/web/copy.go`
