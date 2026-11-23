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
2. Every form the server sends back (a refused send, a missing or
   unparsable recipient, a signature change) MUST carry the message's
   uploads. Files the form carried itself, sent without the script's
   upload, are put in the session first, as uploads are.
3. The form MUST hold each upload as a ticked `upload` checkbox; one sent
   unticked is not attached. Uploads the script adds travel in
   `attachment-uuids`.

## Consequences

- A refusal and a retry keep what was attached.
- An upload of a send that failed, or one unticked, stays with the
  session until the user removes it with the script or the session
  ends, and counts against the session's `MaxAttachmentSize` (32 MiB)
  until then.

## References

- ADR 13, ADR 40, ADR 65
- `mail/web/compose.go` (`submit`, `Uploads`, `handleCancelAttachment`),
  `mail/web/share.go` (`putAttachments`), `templates/pages/mail/compose.html`,
  `mail/web/images.go` (`imageSource`), `web/session.go`
  (`putAttachment`, `peekAttachment`, `popAttachment`,
  `MaxAttachmentSize`)
