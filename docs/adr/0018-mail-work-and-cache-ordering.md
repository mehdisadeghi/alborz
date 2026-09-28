# 18. Interactive mail stays ahead of speculative work

Status: accepted.

## Decision

Each login (an account signed in) has two ordinary IMAP connections,
opened as needed, plus the IDLE watcher. Connection choice is explicit:

- Foreground reads and all writes use the interactive connection.
- Body warming, next-page metadata and long scans use the work connection.
- A reader joining a speculative listing still queued withdraws it and
  fetches the page itself on the interactive connection; one already
  running is joined. An already-running command finishes before another starts.
- Signing in shares its INBOX fetch with the first page on the
  interactive connection. The merged INBOX reuses those rows.

Speculative page loads fetch metadata only. Body prefetch starts when a
page is visited; fetching unvisited pages' bodies increased traffic and
evicted bodies the reader was still using. The next-page metadata stays
off the interactive connection so opening a message cannot queue behind it.

Shared listing fetches have a bounded lifetime independent of their
first reader.

## Priority and cancellation

Every call names a class (`IMAPClass`), which picks the connection and
the place in its queue:

| Class | Connection | Bound | Used for |
|---|---|---|---|
| `IMAPForeground` | interactive | 10 s | pages, writes |
| `IMAPScan` | work | 2 min; 10 for an mbox import | searches of message text, selections, mbox export and import |
| `IMAPBackground` | work | 10 s | body warming, next-page metadata, counts, samples |

Each connection has one queue (`imap_queue.go`). A caller holds the
connection for one callback; on release the first waiting call that
is not background goes next, else the first background one. A scan
goes ahead of queued body fetches but waits for the batch holding the
connection. On the interactive connection every call is foreground and the
queue is plain first come, first served. A queue holds at most 64
waiters; the next caller is refused at once rather than waiting behind
a slow server.

The bound covers waiting and running together, from the call. A call
still waiting when the bound runs out reports the server as slow
(ADR 33); one whose reader left returns cancelled, and in both cases
it leaves the queue without touching the connection. A speculative
listing carries a start check (`WithIMAPStart`), asked once it holds
the connection: if a reader took the page over meanwhile, it gives the
connection back unused.

Once a callback runs, the reader leaving no longer stops it: a command
half sent would leave the connection unusable. It drains under the
call's deadline. Only that deadline, or the login ending, stops it;
then the connection is closed and the next call dials a new one.

## Write and invalidation rules

Upstream confirmation precedes cache updates. Keep a mutation and its
cache confirmation within the same interactive connection callback.

| Change | Required cache operation |
| --- | --- |
| Automatic read after opening a cached body | Register a pending token before returning the page; check it after acquiring the interactive connection; STORE, then update listing and body flags before releasing the connection. |
| Explicit read/unread or replacement flags | Cancel pending automatic-read tokens before queueing the write. On success call `listings.flags`, which updates body flags too. |
| Other confirmed flag edit | Call `listings.flags`. Plain pages are patched; filtered and merged listings are invalidated. In-flight body fetches for affected UIDs lose their tokens. |
| Move, delete, or folder membership change | Invalidate affected listings with `evict` or `evictAll`; `bodies.discard` removes bodies and pending fetch/read tokens (the whole mailbox for EXPUNGE, which may remove other deleted messages too). Destination UIDs identify different cached bodies. |
| IDLE arrival or reconnect gap | Mark listings stale, retain the previous snapshot, and replace it after successful refresh. Failed refreshes leave the old snapshot available. |
| UIDVALIDITY changes | `bodies.observe` discards old bodies, in-flight body tokens and pending read tokens for that mailbox. Mailbox identity is independent of listing eviction. |
| Sign out | `forgetAccount` drops listings, bodies, pending reads, mailbox identities and account memos. |

Listing invalidation increments an account generation. Both shared-flight
identity and publication use that generation: an old fetch cannot repopulate
an invalidated cache or satisfy a new-generation reader. Bodies use fetch
tokens for the same reason. Memo writes increment a version so an older
background refresh cannot overwrite a confirmed local update.

Navigation fragments fetch only missing headers and envelope data. They
never fetch message bodies or mark a message read.

## Limits and consequences

A clicked page waits only for a speculative fetch of that same page
already on the wire, never for a body batch.
The IMAP queue is bounded. Body storage is limited per account and by a
shared byte budget; listing storage has a shared entry limit. These limits
do not represent a measured process RSS limit.

DAV has independent per-host limits, with half the transport capacity
reserved for foreground requests. Collection fan-out and transport limits
are separate controls. Foreground account discovery has its own deadline.

Cached displays can lag upstream state while revalidation runs. The tests
cover shared-fetch cancellation, invalidation generations,
mailbox identity, flag updates and memo write ordering. The local latency
benchmark measures IMAP journeys; it does not establish production behaviour
for METADATA, THREAD, full-text indexing, multiple accounts or DAV servers.
