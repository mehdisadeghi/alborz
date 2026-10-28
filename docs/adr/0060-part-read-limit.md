# ADR 60: Part read limit

Status: accepted.

## Context

Pages that show or parse a MIME part (text or HTML body, HTML pieces
joined around inline images, quoted text in a reply or forward, the
text/calendar part of an invitation, signature and key parts)
fetched the whole part. A part of many megabytes cost transfer, memory
and browser rendering for text nobody reads on screen.

## Decision

1. One limit, `partReadMax` = 2 MiB of the part as transferred
   (before Content-Transfer-Encoding decoding), applies to every part
   read to show or to parse.
2. A shown part MUST be fetched with a partial body section,
   `BODY.PEEK[<part>]<0.2097152>` (RFC 9051 6.4.5). BINARY (RFC 3516)
   is not used for this read.
3. Whether a part is cut MUST come from its size in BODYSTRUCTURE, not
   from the number of bytes returned.
4. A cut part MUST end at the last line break of the fetched bytes, so
   base64 and quoted-printable decode, and the decoded text MUST end at
   the last whole UTF-8 character. The HTML sanitizer closes elements
   the cut left open.
5. The message page MUST show a notice under a cut part, with a link
   to download the whole part.
6. A part over the limit MUST NOT be parsed: an invitation part gets
   no card and stays an attachment; a multipart/signed signature part
   (RFC 1847) over it is not verified, and an application/pgp-keys part
   over it is not read.
7. A reply or forward quotes the cut text as is, without a marker.

## Consequences

- Downloads, the zip, the .eml, the source view, mbox export,
  forwarding as an attachment and images inlined in compose read
  whole parts. A single-part download uses BINARY when the server
  offers it.
- Printing the message page prints the cut text and the notice.

## References

- RFC 9051 6.4.5, RFC 3516, RFC 2045 6, RFC 1847
- ADR 43
- `mail/imap.go` (`partReadMax`, `IMAPPartNode.Cut`, `getShownPart`,
  `partFetchOptions`, `readPart`, `wholeRunes`, `CutPart`),
  `mail/reading.go` (`CalendarPart`, `StreamPart`), `mail/pgp.go`
  (`verifySignature`),
  `mail/send.go` (`Quotable`)
- `templates/pages/mail/message.html` (`part-cut`), locale
  `message.cut`
