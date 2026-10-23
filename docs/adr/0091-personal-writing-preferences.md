# ADR 91: Start page and writing preferences

Status: accepted.

## Decision

- Store the default section in the shared profile. Root navigation and sign-in
  use it; explicit links and a safe `next` destination retain precedence.
  Mail remains the default for old profiles and unknown future values.
- Offer account-labelled signatures from all signed-in accounts only when
  pooling is enabled. Definitions and defaults remain owned by their accounts.
  An unavailable selection is refused with the editor's text retained.
- Add the default-on, optional `Sent by Alborz Webmail` footer to a copy of a
  composed message immediately before submission. It is independent of
  signatures and never modifies drafts, invitation messages or a refused editor.
- Partial profile updates do not implicitly disable these new checkboxes.

## Consequences

The attribution can be disabled globally. Pooling does not copy signatures
between stores or change an account's default. The sender remains the account
the reader chose, regardless of which account owns a selected signature.
