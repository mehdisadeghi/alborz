# ADR 91: Start page and writing preferences

Status: accepted. Amends ADR 52.

## Context

Readers who use Alborz primarily for calendar, tasks or contacts want
root navigation and sign-in to open that section by default. In
multi-account sessions, writers also want to pick signatures across
signed-in accounts and control whether compose offers a client
attribution line.

## Decision

1. `Profile` (ADR 52) stores the default start section (`StartPage`).
   `/` and post-login redirects open that section unless an explicit
   link or valid `next` parameter was given. Unknown or empty values
   default to mail.
2. When signature pooling (`PoolSignatures` in `Profile.Writing`) is
   enabled, compose offers signatures from all signed-in accounts,
   labelled by account. Signatures and default signature choices remain
   stored per account (`mail.Settings`). Selecting a signature that no
   longer exists refuses the submission and preserves the composed text.
3. Unless `NoAttribution` in `Profile.Writing` is set, compose pre-fills
   the `compose.attribution` line, in the interface language, as the last
   line of the sign-off under the `-- ` delimiter (RFC 3676 4.3): under
   the signature when there is one, else alone. It applies to new mail,
   replies and forwards and stays editable; sending MUST NOT add it.
4. The sign-off sits under the writing: above the quote when the reply
   is written on top and above a forwarded message, under the writing
   otherwise. A signature change MUST replace a sign-off only when it
   stands exactly as an offered signature left it, once: in plain text
   as whole lines from the delimiter, in HTML as the paragraph's content.
   With no delimiter in the body the new sign-off is added, on top when a
   quote or forwarded message is under the writing. Otherwise the body
   MUST NOT change and the writer is told the edited sign-off was kept.
5. Partial profile form submissions MUST NOT reset checkboxes that were
   not rendered on the submitted form.

## Consequences

- Signature pooling reads other signed-in accounts' settings at compose
  time without copying signatures between stores or changing the `From`
  account.
- What the writer sees is what is sent; drafts keep the line as typed.
- The Settings toggle decides only whether the line is pre-filled.
- A signature switch never cuts writing or quotes; an edited sign-off,
  or one written under another interface language, stays until the
  writer changes it by hand.
- A sign-off also present in a forwarded message is ambiguous and is
  left alone.

## References

- RFC 3676 (format=flowed, signature delimiter)
- ADR 1, ADR 52
- `profile.go`, `web/start.go`, `settings/web/profile.go`,
  `mail/web/compose.go`, `mail/web/writing.go`, `mail/send.go`,
  `mail/richtext.go`
