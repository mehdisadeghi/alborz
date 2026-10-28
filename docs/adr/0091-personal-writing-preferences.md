# ADR 91: Start page and writing preferences

Status: accepted. Amends ADR 52.

## Context

Readers who use Alborz primarily for calendar, tasks or contacts want
root navigation and sign-in to open that section by default. In
multi-account sessions, writers also want to pick signatures across
signed-in accounts and control whether outgoing mail appends a client
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
3. When attribution (`Attribution` in `Profile.Writing`, on by default)
   is enabled, sending appends a `Sent by Alborz Webmail` footer to the
   outgoing copy immediately before SMTP submission. It MUST NOT modify
   saved drafts, calendar invitation messages or a re-rendered compose
   form.
4. Partial profile form submissions MUST NOT reset checkboxes that were
   not rendered on the submitted form.

## Consequences

- Signature pooling reads other signed-in accounts' settings at compose
  time without copying signatures between stores or changing the `From`
  account.
- The attribution line is controlled globally in Settings and never
  pollutes drafts.

## References

- ADR 1, ADR 52
- `profile.go`, `web/start.go`, `settings/web/profile.go`,
  `mail/web/compose.go`, `mail/send.go`
