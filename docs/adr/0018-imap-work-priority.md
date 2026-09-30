# ADR 18: IMAP work priority and cache ordering

Status: accepted.

## Context

Speculative work (body prefetch, next-page metadata, long SEARCHes)
shares a login's IMAP connections with the reader's own requests. It
must not delay them, and no cache write may overwrite newer state.

## Decision

### Connections

1. A login has two IMAP connections, opened on demand, plus the IDLE
   watcher: interactive (foreground reads, all writes) and work
   (speculative work and scans).
2. Every call names a class (`IMAPClass`):

   | Class | Connection | Bound | Used for |
   |---|---|---|---|
   | `IMAPForeground` | interactive | 10 s | pages, writes |
   | `IMAPScan` | work | 2 min; 10 min for an import | SEARCH of message text, selections, mbox import |
   | `IMAPBackground` | work | 10 s | body prefetch, next-page metadata, counts |

3. Each connection has one queue. A caller holds the connection for
   one callback. On release, the first waiting non-background call
   runs next, else the first background one. A queue holds at most 64
   waiters; further callers are refused immediately.
4. The bound covers waiting and running, from the call. A call still
   queued at its bound reports the server as slow (ADR 33); one whose
   reader left returns cancelled. Neither touches the connection.
5. Once a callback runs, the reader leaving does not stop it: a
   half-sent command would leave the connection unusable. Only the
   bound or the login ending stops it; the connection is then closed
   and the next call dials a new one.
6. Speculative listings carry a start check (`WithIMAPStart`): if a
   reader took the page over while it queued, it releases the
   connection unused. A reader joining a queued speculative listing
   withdraws it and fetches on the interactive connection; one already
   running is joined.
7. Speculative page loads fetch metadata only. Body prefetch starts
   when a page is visited. Sign-in shares its INBOX fetch with the
   first page. Navigation fragments MUST NOT fetch bodies or set
   `\Seen`.

### Transfers

8. Downloads (a part, a message, its source, attachments as zip, mbox
   export) and signature checks stream from the server's literal and
   run on connections of their own (`DoIMAPTransfer`), at most two per
   login (Dovecot's default is ten connections per user and IP).
9. A transfer has no total bound: it ends when the reader leaves, the
   login ends, or the server is silent for 30 s. Its connection is
   kept for the next transfer and not reused after 15 s idle.

### Cache writes

10. The server's confirmation MUST precede a cache update, within the
    same interactive callback as the write.

    | Change | Cache operation |
    |---|---|
    | Automatic `\Seen` on opening a cached body | Register a pending token before returning the page; check it on the interactive connection; STORE, then update listing and body flags before release. |
    | Explicit read/unread or flag replacement | Cancel pending automatic-read tokens first; on success `listings.flags` (updates bodies too). |
    | Other confirmed flag change | `listings.flags`: plain pages patched, filtered and merged listings invalidated; in-flight body fetches for those UIDs lose their tokens. |
    | MOVE, EXPUNGE, mailbox membership change | `evict`/`evictAll` the listings; `bodies.discard` the bodies and tokens (the whole mailbox for EXPUNGE). |
    | IDLE arrival or reconnect | Mark listings stale, keep serving the previous snapshot, replace it on successful refresh. |
    | UIDVALIDITY change | `bodies.observe` discards the mailbox's bodies and tokens. |
    | Sign out | `Mailboxes.ForgetAccount` drops listings, bodies, tokens, UIDVALIDITY records and account memos. |

11. Listing invalidation increments a per-account generation used for
    both fetch sharing and publication, so an older fetch cannot fill
    an invalidated cache. Bodies use fetch tokens; memos a version, so
    an older refresh cannot overwrite a confirmed update.

## Consequences

- A clicked page waits only for a speculative fetch of the same page
  already on the wire, never for a body batch.
- Cached views can lag the server while revalidation runs.
- Bodies: 100 per account within 64 MiB shared; listings: 512 entries
  shared. These are not a measured memory limit.
- DAV is limited separately: 4 connections per host, 2 of them for
  background work.

## References

- RFC 9051 (FETCH, STORE, EXPUNGE, IDLE, UIDVALIDITY)
- ADR 6, ADR 7, ADR 20, ADR 33
- `login.go` (`IMAPClass`, `DoIMAPWork`, `DoIMAPTransfer`,
  `maxIMAPTransfers`, `ScanTimeout`, `RoundTripTimeout`),
  `imap_queue.go` (`imapQueueLimit`, `WithIMAPStart`), `imap.go`
  (`transferStall`), `mail/cache.go`, `mail/bodies.go`,
  `mail/cacheevents.go`, `mail/prefetch.go`, `mail/mbox.go`
  (`importBound`), `internal/memo`, `remote.go` (`connectionsPerHost`)
