# ADR 38: Upload lifetime in compose

Status: accepted.

## Context

The compose form uploads files ahead of sending; the session holds
each under an id. If the send removed the uploads before SMTP
submission, a refused send would re-render the form without them and
the next Send would deliver the message without its attachments.

## Decision

1. The session MUST keep an upload until the message is submitted or
   the draft is saved successfully. Only then are its uploads (files
   and inline images) released.
2. A refused send, or one missing a recipient, MUST re-render the form
   with its uploads, ids filled in; the script treats them as finished
   uploads the user can still remove.

## Consequences

- A refusal and a retry keep what was attached.
- An upload of a send that failed stays with the session until the
  user removes it or the session ends, and counts against the
  session's `MaxAttachmentSize` (32 MiB) until then.

## References

- ADR 40
- `mail/web/compose.go` (`submit`, `Uploads`, `handleCancelAttachment`),
  `mail/web/images.go` (`imageSource`), `web/session.go`
  (`putAttachment`, `peekAttachment`, `popAttachment`,
  `MaxAttachmentSize`)
