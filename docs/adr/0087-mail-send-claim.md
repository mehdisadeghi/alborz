# ADR 87: Claim a compose send before SMTP

Status: accepted.

## Context

SMTP submission and IMAP Sent filing cannot share a transaction. If a
Sent folder is missing or a network failure interrupts SMTP `DATA` or
post-send IMAP filing, retrying the compose POST can send a duplicate
message.

## Decision

1. Every rendered compose form MUST include its `Message-ID`. Before
   dialling SMTP, alborz MUST atomically claim `account + "\x00" +
   messageID` in `alborz.db` under `alborz.mail.send/<keyed name>`
   (ADR 51, ADR 70) and verify the account's Sent mailbox via IMAP
   `LIST`. If the Sent lookup fails, the claim MUST be released.
2. An existing claim MUST block a second SMTP submission:
   - a completed claim answers as already sent;
   - a claim still in `DATA` answers that acceptance is uncertain
     (`notice.senduncertain`);
   - a claim accepted by SMTP with incomplete IMAP follow-up answers
     with `notice.sentfollowup`.
   Claims expire 10 minutes (`sendClaimKeep`) after their last state
   change; active in-flight claims never expire.
3. A failure before SMTP `DATA` begins MAY delete the claim so the
   reader can fix and retry the form. Once `DATA` begins, the claim MUST
   be kept because server acceptance may be unknown. A 2xx response to
   `DATA` counts as accepted even if `QUIT` fails.
4. Immediately after `DATA` acceptance, the claim MUST be marked
   accepted before IMAP follow-up (marking the replied/forwarded
   message, appending to Sent, deleting the draft). Follow-up failures
   MUST be reported as post-send warnings, never as a failed send, and
   the draft MUST be kept if filing to Sent fails.
5. Each new claim lazily sweeps expired non-active claims in the same
   database transaction.

## Consequences

- Retrying a compose submission within 10 minutes after a dropped
  connection or crash will not send a second copy.
- When a failure occurs during `DATA`, the reader is asked to check Sent
  or the recipient before sending again.

## References

- RFC 5321 (SMTP `DATA`), ADR 51, ADR 65, ADR 70
- `mail/send_claim.go`, `mail/send.go`, `mail/smtp.go`,
  `mail/web/compose.go`
