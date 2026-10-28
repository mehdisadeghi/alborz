# ADR 40: Inline images in composed mail

Status: accepted.

## Context

The editor dropped pasted images, replies quoted HTML without its
images, and forwards sent only the plain text of an HTML message.

## Decision

1. An `img` in the editor MUST reference one of two local sources;
   the editor's policy (`ComposedPolicy`) and the server allow no
   other `src`:
   - an upload the session holds: `/compose/attachment/<uuid>`
     (route `mail.attachment`), created by pasting or dropping an image;
   - a part of a message the account holds:
     `/message/<mailbox>/<uid>/raw?[account=<user>&]part=<path>`
     (route `mail.message.raw`), which a `cid:` or `part:` reference in
     a quoted, forwarded or reopened message is rewritten to.
2. Remote images MUST NOT enter a composed message.
3. An upload is served inline only as PNG, JPEG, GIF or WebP; any other
   type is served as `attachment`.
4. On send, each distinct referenced image becomes a MIME part:
   - HTML goes out as `multipart/related` (RFC 2387) with the images,
     each with `Content-Disposition: inline` and a Content-ID
     `<n>.<message-id>` that the `img` names by `cid:`;
   - `text/plain` and the related part form a `multipart/alternative`;
   - files follow in `multipart/mixed`.
   An upload referenced by the HTML is not also sent as a file.
5. A draft stores its images the same way. Reopening a draft points the
   editor at the draft's own parts; sending fetches them before the
   draft is deleted.
6. A reply quotes the original HTML with its images. A forward of an
   HTML message is written in the editor: separator, headers, then the
   original HTML under the same policy, images kept. Without script the
   forward is plain text.

## Consequences

- Markup outside the composed policy (tables, fonts, colours, layout)
  is lost in quotes and forwards.
- Images in Apple Mail's split HTML (ADR 39) are quoted by part path.
- Inline uploads count toward the session's 32 MiB attachment limit.

## References

- RFC 2045, RFC 2046, RFC 2387, RFC 2392 (`cid:`)
- ADR 38, ADR 39
- `mail/richtext.go` (`ComposedPolicy`), `mail/inline.go`
  (`InlineImages`, `PartImage`, `cidImages`, `localImages`),
  `mail/smtp.go` (`writeAlternative`, `writeRelated`),
  `mail/send.go` (`QuotedHTML`), `mail/web/images.go` (`localImage`,
  `imageSource`, `handleGetAttachment`, `shownTypes`),
  `mail/web/compose.go` (`forwardHTML`, `quotedHTML`),
  `web/session.go` (`MaxAttachmentSize`), `templates/assets/editor.js`
