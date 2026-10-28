# ADR 25: Passkey lock for a browser session

Status: accepted, amended by ADR 29 (session cookie secret), ADR 95
(cross-tab lock coordination and concealment) and ADR 97 (60-day
remembered session inactivity).

## Context

A remembered session holds its accounts' passwords sealed under the
secret in the browser's cookie (ADR 5, ADR 29). A WebAuthn passkey
cannot authenticate directly to IMAP, but it can gate access to the
session's held credentials in that browser.

## Decision

1. A browser session MAY register one or more passkeys (WebAuthn Level 2,
   user verification required), stored in `SessionRecord.Lock` with the
   session ID as the WebAuthn user handle.
2. A session with a passkey locks after the configured inactivity period
   (default 30 minutes, minimum 1 minute), on clicking "Lock" (ADR 95),
   and on server restart.
3. While locked:
   - every app request redirects to `/unlock` (`?next=` on GET), while
     unasked background requests (`Alborz-Unasked`) receive 204;
   - remembered accounts are not restored;
   - `/events` sends `event: locked` and closes;
   - tabs conceal their content and coordinate state per ADR 95.
4. A valid WebAuthn assertion unlocks the session. `/unlock/leave` signs
   out and destroys the session when passkeys are unavailable.
5. Only user-driven requests (`Activity` middleware) and `/alive`
   heartbeats (posted at most once per minute on key or pointer input)
   count as activity.
6. Every response to a passkey-protected session MUST set
   `Cache-Control: no-store`, purging and disabling service-worker offline
   page storage (ADR 19, ADR 90).
7. The WebAuthn Relying Party ID is the request host without port, and
   the origin is the request scheme and host.
8. `/settings/passkeys` lists and removes passkeys and states what the
   browser lock does not cover (direct IMAP/SMTP/DAV access, other
   browsers, unlocked tabs, and browser password managers).

## Consequences

- Passkey lock protects an unattended or restarted browser without
  requiring a mail-server WebAuthn extension.
- Passkey-protected sessions store no offline pages in the service
  worker.

## References

- WebAuthn Level 2; ADR 5, ADR 19, ADR 29, ADR 90, ADR 95, ADR 97
- `lock.go`, `web/lock.go`, `web/middleware.go` (`Activity`, `Lock`),
  `auth/web/lock.go`, `settings/web/passkeys.go`
