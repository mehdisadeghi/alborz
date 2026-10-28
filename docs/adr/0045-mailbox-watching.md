# ADR 45: Watching every mailbox

Status: accepted. Amends ADR 6.

## Context

The watcher holds one IMAP connection per account in IDLE (RFC 2177)
on INBOX; listings are marked changed and open pages told through it.
IDLE reports only the selected mailbox, so mail filed elsewhere by
Sieve (RFC 5228) rules is not noticed.

## Decision

1. The watcher selects INBOX read-only and, where the server advertises
   NOTIFY (RFC 5465), sends `NOTIFY SET` with:
   - `SELECTED`: MessageNew, MessageExpunge, FlagChange;
   - `PERSONAL`: MessageNew, MessageExpunge.
2. An unsolicited STATUS for another mailbox marks that mailbox's
   listings changed and announces it by name.
3. Without NOTIFY, IDLE stays on INBOX. The watcher takes a baseline
   UIDNEXT of every selectable mailbox, then after each IDLE wake (news
   or `sweepWindow`, 2 minutes) issues STATUS (UIDNEXT) for each and
   announces those that changed. Mailboxes with `\Noselect` or
   `\NonExistent` are skipped.
4. Without IDLE, the same sweep runs every `idleWindow` (20 minutes).
5. On NOTIFICATIONOVERFLOW the watcher drops every cached listing of the
   account and reconnects, which re-arms NOTIFY.
6. Every connection of the watcher, the first included, drops the
   account's cached listings once it is armed and warms INBOX again:
   a listing read before the watch, the sign-in's first page among
   them, would otherwise miss what arrived until it was armed.

## Consequences

- With NOTIFY, mail filed by a rule is announced under its mailbox.
- Without NOTIFY, detection lags up to `sweepWindow` and costs one
  STATUS per mailbox per sweep on the held connection.
- The page is unchanged: the open mailbox refreshes, others update
  their counts.

## References

- RFC 2177, RFC 5465, RFC 9051 section 6.3.11 (STATUS)
- ADR 6, ADR 18
- `mail/watch.go` (`follow`, `armNotify`, `folderSweep`, `sweepEvery`,
  `idleOnce`, `sweepWindow`, `idleWindow`), `mail/cacheevents.go`
  (`accountChanged`)
