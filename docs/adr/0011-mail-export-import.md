# ADR 11: Mail export and import

Status: accepted.

## Context

Messages leave as an mbox and arrive from an mbox or a single RFC 5322
message. An archive MAY leave without its attachments. An import MUST
NOT duplicate messages and MUST NOT fill the server's disk. There is no
job runner: the work lives inside one request.

## Decision

### Export

1. Export takes the selection, the whole mailbox, or all results of a
   search or view. The selection downloads at once; a whole mailbox or
   search goes through a confirmation page showing the message count
   and, where the server has STATUS=SIZE, the total size.
2. The mbox (mboxrd) is streamed per message on a transfer connection
   (ADR 18); nothing is held in memory beyond one message. Response
   headers are sent with the first message, so a failed first FETCH
   is an error page, not a broken file.
3. "Without attachments" rewrites each message: every MIME part that
   is not text, and every text part that is an attachment (ADR 39),
   is replaced by a `text/plain` part `[<note>: <filename>, <type>,
   <size> B]`.
4. A top-level `multipart/signed` or `multipart/encrypted` message
   MUST be exported whole. A message that fails to parse is exported
   whole and the failure logged.

### Import

5. The import form reads an mbox or a single message into a mailbox
   it creates and names (default: the file name); the server's refusal
   of CREATE for a taken name is the guard.
6. Files dropped on a mailbox in the list are appended to that
   mailbox. Message-IDs it already holds are read first and skipped,
   so a repeated drop adds nothing.
7. The upload is read as a multipart stream and each message sent with
   one APPEND; nothing is written to local disk. The request body is
   capped at 1 GiB.
8. Messages are appended with `\Seen` and an internal date taken from
   their Date header. Within one file, a repeated Message-ID is
   skipped.
9. An import holds the work connection once (`IMAPScan`, 10 min). An
   import that stops leaves the new mailbox with the messages appended
   so far and a notice with the count.

## Consequences

- A large import is one long POST with no progress; a proxy with a
  short timeout cuts it and the notice reports the count.
- Re-running a form import under the same name is refused; a
  repeated drop adds nothing.
- Gmail labels are not read.

## References

- RFC 9051 (APPEND, CREATE, FETCH), RFC 8438 (STATUS=SIZE), RFC 1847
- ADR 18, ADR 39
- `mail/mbox.go` (`ExportRun`, `withoutAttachments`, `ImportFile`,
  `DropFiles`, `ExportSize`, `importBound`), `mail/web/archive.go`
  (`handleExportMbox`, `handleExportPage`, `handleImport`,
  `handleDropImport`, `importMaxSize`)
