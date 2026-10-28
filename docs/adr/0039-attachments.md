# ADR 39: What counts as an attachment

Status: accepted, amended by ADR 43 (S/MIME signatures are verified).

## Context

Senders mark files in more ways than `Content-Disposition: attachment`
(RFC 2183). Apple Mail sends every file inside the HTML branch as
`inline` with a filename; other mailers give only a Content-Type `name`
or no name at all. A part that is neither listed as a file nor shown as
the message is lost to the reader, and a text file taken for the body
replaces the message text.

## Decision

1. One predicate decides whether a MIME part is a file or part of the
   message. The attachment list, the list row's attachment mark, the
   attachments zip, the body part selection (text and HTML) and the
   mbox export without attachments MUST all use it.
2. The rules apply in order:
   1. The signature part of a `multipart/signed` (RFC 1847), with type
      `application/pgp-signature`, `application/pkcs7-signature` or
      `application/x-pkcs7-signature`, is part of the message.
   2. `Content-Disposition: attachment` is a file.
   3. A part with a Content-ID directly inside `multipart/related`
      (RFC 2387) is part of the message; the HTML shows it by `cid:`.
   4. A part with a filename, from the disposition's `filename` or the
      Content-Type's `name`, is a file.
   5. A `text/*` part without a name is part of the message; any other
      part is a file.
3. A file without a name is shown as "Unnamed file" and zipped as
   `part` plus its media type's extension. An unnamed
   `message/rfc822` part is named after its Subject, on the page and
   in the zip.
4. When an HTML part is split around inline images inside
   `multipart/mixed` (Apple Mail's shape), the HTML pieces MUST be
   fetched in one FETCH and joined; an image between them is rendered
   in place and referenced by part path (`part:2.2`). Only parts of the
   same message resolve.
5. In the text view, images beside the text part that the rules make
   part of the message are shown after the text. Images under an HTML
   alternative are not.

## Consequences

- Inline files with a filename, name-only parts, unnamed non-text
  parts and forwarded messages are listed; text files marked `inline`
  or named only by `name` are no longer taken for the body.
- A delivery report's `message/delivery-status` and returned message
  are listed as files.
- Inline images referenced by Content-ID never mark a row as carrying
  a file and stay out of the zip.
- RFC 2231 names are decoded by the server; RFC 2047 names by go-imap.

## References

- RFC 2045, RFC 2046, RFC 2183, RFC 2231, RFC 2387, RFC 1847
- ADR 40, ADR 43
- `mail/imap.go` (`attached`, `signatureTypes`, `fileParts`,
  `Attachments`, `TextPart`, `HTMLPart`, `ShownImages`, `HTMLPieces`,
  `newIMAPPartNode`), `mail/reading.go` (`joinedHTML`,
  `writeAttachments`, `zipEntryName`), `mail/mbox.go`,
  `mail/web/viewtext.go`, `mail/web/message_test.go`
