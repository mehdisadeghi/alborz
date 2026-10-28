# ADR 65: Private data on disk

Status: accepted, amended by ADR 70 (own collections sealed, every key
naming private data a keyed hash) and ADR 72 (an account's own key).

## Context

Mail listings and bodies are held in memory only. DAV objects are kept
on disk as the synced copy (ADR 53). This ADR states one rule for
both. Pages the browser keeps (ADR 19) are out of scope.

## Decision

1. Private data MAY be written to the server's disk only when both hold:
   - it is sealed under a key derived from the login key (ADR 70), or,
     for an account with encryption on, under its own or its
     collection's key (ADR 72), and
   - it is a copy that can be deleted and fetched again from its server.
2. Own collections (ADR 24) are exempt from the second condition:
   alborz is their server, so the sealed record is the only copy. Their
   way out is the account's archive (ADR 70).
3. Mail is kept in memory by choice. A sealed, droppable copy of mail
   would satisfy rule 1.
4. A new on-disk cache of private data MUST satisfy rule 1 or have an
   ADR of its own.
5. Temporary files are no exception. A message being sent or saved,
   the originals it forwards, and every file a form uploads are
   written to a sealed spool (`seal.Spool`): a temp file unlinked on
   creation, sealed under a random key only memory holds, chunked as
   STREAM (64 KiB AES-256-GCM chunks, nonce = chunk index and a
   last-chunk flag), so a chunk moved, dropped or cut short does not
   open. Forms are read part by part (`web.ReadForm`); net/http's
   `ParseMultipartForm`, which spills files unsealed to the temp dir,
   is never called.

## Consequences

- The DAV mirror (`caldav.db`, `carddav.db`) and the records in
  `alborz.db` satisfy the rule.
- A spool dies with the process: its key is gone, and on Unix so is
  the file. It does not guard against a reader of the process's memory
  or swap.

## References

- ADR 19, ADR 24, ADR 53, ADR 70
- `internal/seal` (`spool.go`), `web/form.go`, `pim/dav/mirror.go` (`OpenMirror`),
  `pim/store/store.go` (`New`), `records.go`
