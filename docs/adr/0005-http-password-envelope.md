# ADR 5: HTTP password envelope

Status: accepted, amended by ADR 29 (the session store holds no key
that opens a password), ADR 68 (password change rewraps the envelope),
ADR 70 (AES-256-GCM behind a format byte) and ADR 72 (sealed under the
account's key while encryption is on).

## Context

A CalDAV or CardDAV source may take a password other than the mail
password. The operator holds the login key, so a value sealed only
under it is readable by the operator.

## Decision

1. The HTTP password MUST be stored only when it differs from the mail
   password. Storing an empty value or the mail password reverts to
   the mail password.
2. It MUST be stored as an envelope under the store key
   `http-password`: the secret sealed under a random 256-bit key, and
   that key sealed under an argon2id key derived from the mail
   password with a 16-byte random salt (time 1, memory 64 MiB,
   4 threads).
3. Nothing but the mail password opens the envelope. It lives in the
   root package, beside the login; no other package handles a password.
4. A mail-password change made through alborz (poppassd) MUST rewrap
   the random key under the new password with a new salt.
5. An envelope the mail password no longer opens (`ErrSealed`) MUST be
   kept. Requests to a named DAV source then fail as refused, as an
   HTTP 401 would, and the user is asked to enter the password again
   or clear it (ADR 59).
6. The envelope MUST NOT be exported or imported with the account's
   settings.

## Consequences

- A mail-password change made elsewhere costs the DAV password once.
- Derivation costs 64 MiB, so the opened password is cached on the
  `Login`.

## References

- RFC 9106 (Argon2), RFC 9110 11.6.1 (401), RFC 7617 (Basic)
- ADR 29, ADR 31, ADR 59, ADR 70
- `secret.go` (`envelope`, `HTTPPassword`, `SetHTTPPassword`,
  `rewrap`), `passwd.go`, `internal/seal/seal.go`,
  `archive.go` (`exportSettings`), `pim/dav/transport.go`
  (`RefusedError`)
