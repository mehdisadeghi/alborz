# ADR 29: Session cookie secret

Status: accepted. Amends ADR 5 and ADR 25.

## Context

The session cookie `alborz_session` is `<id>.<secret>`. The settings
page `/settings/sessions` shows the ids of an account's other sessions,
so the id alone is not a credential.

## Decision

1. A session MUST store a proof of its secret:
   `HMAC-SHA256(key=secret, "alborz session proof")`, in memory and in
   `SessionRecord.Proof`.
2. A lookup MUST recompute the proof from the cookie and compare it in
   constant time. A mismatch MUST be treated as an unknown session.
3. The proof MUST NOT be `SHA-256(secret)`: that value is the key that
   seals the session's passwords (`sessionKey`), and the store MUST NOT
   hold it (ADR 5).
4. A record without a proof opens no session; that browser signs in
   again.
5. The cookie is not signed; the proof already binds it to the session.

## Consequences

- Session ids MAY be shown to other sessions of the account.
- Records written before the proof require one new sign-in.

## References

- ADR 5, ADR 25, ADR 26
- `web/session.go` (`lookupSession`, `sessionProof`, `sessionKey`),
  `records.go` (`SessionRecord`)
