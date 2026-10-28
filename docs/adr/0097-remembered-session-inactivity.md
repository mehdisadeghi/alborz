# ADR 97: Remembered session inactivity

Status: accepted. Amends ADR 25, ADR 59, ADR 70 and ADR 74.

## Context

The sign-in "remember me" option previously expired cookies and sealed
credentials a fixed 30 days after creation even when the reader used
Alborz daily, forcing monthly password prompts on active devices.

## Decision

1. The sign-in form offers "Keep me signed in on this device"
   (`login.remember`) with a stated 60-day inactivity limit
   (`credentialCookieLife = 60 * 24 * time.Hour`). The same policy
   applies in browser tabs and installed PWAs.
2. A remembered session has one persisted expiry shared by its session
   cookie and its sealed account credentials. Authenticated user-driven
   activity (`Activity` middleware) advances that expiry to 60 days from
   now.
3. Background requests (`Alborz-Unasked`), SSE streams, push delivery
   and lock probes MUST NOT advance the expiry.
4. Session lookup and account restoration MUST check expiry before
   restoring or refreshing any session. Renewal write failures MUST NOT
   issue a cookie with a longer lifetime than the persisted record.
5. Explicit sign-out, remote session revocation (`/settings/sessions`),
   and password changes (ADR 68) immediately revoke remembered access.
   Passkey session locking (ADR 25, ADR 95) remains independent.
6. Renewal MUST NOT revive expired sessions or credentials.

## Consequences

- Active remembered devices stay signed in across restarts without
  periodic password prompts; devices unused for 60 days expire.

## References

- ADR 25, ADR 29, ADR 59, ADR 68, ADR 70, ADR 74, ADR 95
- `server.go` (`credentialCookieLife`, `CredentialCookieLife`),
  `records.go` (`SessionRecord`), `web/session.go`,
  `web/middleware.go` (`Activity`), `auth/web/login.go`
