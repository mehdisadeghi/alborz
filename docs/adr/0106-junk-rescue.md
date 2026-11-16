# ADR 106: Moving mail from contacts out of Junk

Status: accepted. Amends ADR 45.

## Context

A server's spam filter files some mail from people the reader knows
in Junk (RFC 6154 `\Junk`), where it goes unread. A From address alone
is forgeable, so knowing the sender is not enough to undo the filter;
the receiving server's Authentication-Results (RFC 8601) under the
account's trusted authserv-id can say whether the From domain stood
behind the delivery.

## Decision

1. While an account's watcher (ADR 45) runs, it MUST record, on each
   connection, the Junk folder's UIDVALIDITY and UIDNEXT. Only
   messages with a UID at or above the UIDNEXT first recorded are
   candidates; a reconnect under the same UIDVALIDITY MUST keep the
   place reached, and a changed UIDVALIDITY starts again from the
   folder's UIDNEXT.
2. A NOTIFY STATUS for Junk, or a sweep that finds Junk's UIDNEXT
   grown, MUST start a rescue. The rescue MUST run on the login's
   background connection (`DoIMAPBackground`, ADR 18), never on the
   watcher's IDLE connection; one rescue runs at a time and nudges
   during it coalesce.
3. A candidate MUST be moved to INBOX only when all hold:
   - its From header holds exactly one address, and that address is in
     one of the account's address books other than Previous
     recipients, or in Previous recipients when the account setting
     `JunkRescuePrevious` is on (off by default: a reply is not trust);
   - the account has a trusted authserv-id, and the topmost
     Authentication-Results under it reports DMARC pass, or DKIM pass
     with `header.d` aligned with the From domain;
   - it is not `\Seen` and carries neither `$Junk` nor `Junk`.
4. A moved message MUST get `$NotJunk` (RFC 5788) before the move where
   the folder's PERMANENTFLAGS hold `\*`, and MUST be moved with UID
   MOVE (RFC 6851), falling back to COPY, STORE and EXPUNGE where MOVE
   is not offered. Both folders' listings are dropped and both are
   announced.
5. The reader's own move into Junk MUST set `$Junk` and clear
   `$NotJunk`/`NotJunk`; a move from Junk to INBOX MUST set `$NotJunk`
   and clear `$Junk`/`Junk`; both only where PERMANENTFLAGS hold `\*`.
6. The account setting `NoJunkRescue` turns the rescue off; an absent
   setting MUST mean on. Messages that arrive while it is off, or
   while no authserv-id is trusted, are passed over for good.
7. The address set MUST be read through `Mailboxes.ContactAddresses`,
   set by the app, so `mail/` imports nothing of `pim/`; it is held
   per account for `contactAddressesTTL` (10 minutes). A failure to
   read it or to move MUST be logged and MUST NOT stop the watcher;
   the candidates are examined again on the next nudge.

## Consequences

- The rescue runs only while a login, or a login kept for push
  notifications, is alive; mail filed in Junk while none is goes
  unexamined.
- Without a trusted authserv-id nothing is moved.
- A contact added or removed takes up to 10 minutes to count.
- Mail from someone the reader only wrote to stays in Junk unless
  `JunkRescuePrevious` is on.
- A rescued message reaches INBOX as new mail and is pushed as such.
- Older Junk content is never touched.

## References

- RFC 8601, RFC 6154, RFC 6851, RFC 5788, RFC 5465, RFC 9051
- ADR 18, ADR 45
- `mail/junkrescue.go`, `mail/watch.go` (`follow`, `folderSweep`),
  `mail/actions.go` (`markJunk`), `mail/authresults.go` (`Vouches`),
  `mail/settings.go` (`NoJunkRescue`, `JunkRescuePrevious`), `mail/mailboxes.go`
  (`ContactAddresses`), `pim/contacts/books.go` (`Addresses`),
  `cmd/alborz/app/routes.go`, `templates/pages/settings/account.html`
