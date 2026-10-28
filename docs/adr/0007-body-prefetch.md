# ADR 7: Warm-up and body prefetch

Status: accepted.

## Context

With caches served first (ADR 6), the remaining waits are the first
page of each section after sign-in and the FETCH that opening a
message costs.

## Decision

1. Signing in, and restoring accounts from the session cookie, MUST run
   the server's `OnAccountReady` hooks. Each package appends its own;
   the core does not know them. They fetch in the background:
   - mail: the INBOX listing at the account's page size, and starts
     the watcher;
   - calendar: events of the current month +/- 7 days, all tasks and
     journal entries (ADR 75);
   - contacts: every address book;
   - filters: a ManageSieve connection (LISTSCRIPTS).
2. After a listing is served, the preferred text part of each row is
   fetched in the background with `BODY.PEEK`, at most 10 messages per
   UID FETCH, skipping parts larger than 512 KiB.
3. Fetched bodies are held in memory keyed by account, mailbox,
   UIDVALIDITY and UID: at most 100 per account and 64 MiB across all
   accounts, each dropped 30 min after its last use. A UIDVALIDITY
   change drops the mailbox's bodies.
4. The message page MUST serve from the held body and ask the server
   only for what it does not contain.
5. Opening a held message sets `\Seen` with a UID STORE that the
   response does not wait for.
6. Opening a message not held sends UID STORE when its fetched flags
   are unread, including when the first request only fetches its header.
7. A UID shown by a listing on another IMAP connection may not yet be
   visible to the selected foreground connection. An empty UID FETCH
   gets one NOOP and retry before the message is reported missing.

## Consequences

- A page's bodies cost a few background round trips instead of one
  FETCH per open.
- A signed message's signature is checked after the page (ADR 60).
- Held bodies do not survive a restart.

## References

- RFC 9051 6.4.5 (FETCH, BODY.PEEK), 6.4.6 (STORE), 2.3.1.1
  (UIDVALIDITY)
- ADR 6, ADR 60
- `mail/bodies.go` (`prefetchBodies`, `markHeldSeen`, `bodyKeepPerUser`,
  `bodyMaxPartSize`, `bodyFetchBatch`, `bodyMaxBytes`), `mail/listing.go`,
  `mail/watch.go` (`Warm`)
- `web/accounts.go` (`AddAccount`, restore), `mail/web/plugin.go`,
  `pim/calendar/caldav.go` (`WarmAccount`), `pim/contacts/web/plugin.go`,
  `filters/web/plugin.go`
