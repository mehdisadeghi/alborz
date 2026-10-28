# ADR 30: Refused-password throttle

Status: accepted.

## Context

The sign-in form, the Basic auth of alborz's own DAV server and the
password change each verify a password with an IMAP LOGIN, which takes
seconds. A check-then-record throttle lets concurrent tries all pass
the check before any refusal is recorded.

## Decision

1. There MUST be one refusal count, `Server.signInRefused`, shared by
   `SignIn`, `CheckPassword` (DAV) and `ChangePassword`.
2. The key is the reader (`readerKey`): the client address when it is
   public; otherwise the address and the lower-cased username.
3. The limit is `signInTries` (5) refusals within `signInWindow`
   (10 min), fail2ban's `maxretry` and `findtime` defaults. Past it,
   the try MUST NOT reach the mail server and fails with `PausedError`.
4. `refusals.attempt` MUST reserve the try under the lock before the
   LOGIN is sent, so concurrent tries are counted as they start.
5. A result that is not an `AuthError` MUST release the reservation: an
   unreachable server is not a wrong password.
6. DAV caches each verdict, accepted or refused, under a salted SHA-256
   of username and password for `davPasswordLife` (5 min). A client
   retrying a stale password costs one try; a corrected password is a
   different digest and is checked at once.
7. A paused DAV client MUST get 429 with `Retry-After`, not 401: a
   client told 401 prompts its user for a new password. The paused
   sign-in form also answers 429.
8. At most `refusalsHeld` (4096) keys are held. When full, expired
   keys are swept once, and not again before the earliest live entry
   expires (`sweepAt`); while still full, a new key is paused until
   then.

## Consequences

- The count is in memory; a restart resets it. The mail server's own
  failed-login limits remain the lasting bound.
- Many users behind one public address share one count.

## References

- RFC 9110 15.5.2 (401), RFC 6585 4 (429), RFC 9110 10.2.3
  (Retry-After), RFC 7617 (Basic)
- `accounts.go` (`refusals`, `readerKey`, `signInTries`,
  `refusalsHeld`, `PausedError`), `passwd.go` (`ChangePassword`),
  `pim/web/owndav.go` (`davPasswords`, `davPasswordLife`, `serveOwn`),
  `web/accounts.go`, `login_test.go`
