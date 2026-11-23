# ADR 95: Session lock coordination

Status: accepted. Amends ADR 25.

## Context

When a browser session protected by a passkey (ADR 25) locks manually or
after inactivity, every open tab and history-restored document of that
session must conceal private content immediately and coordinate lock and
unlock state without exposing session secrets.

## Decision

1. The header's `Lock` button is the single lock control. It locks the
   current browser session on the server without signing out its
   accounts or replacing its cookie, and is rendered only when at least
   one passkey is registered.
2. Authorization remains server-side: protected routes and `/events`
   enforce the session lock, and an uncached state probe
   (`Alborz-Unasked`) reports only the public session ID and lock state
   without extending idle activity.
3. Protected pages MUST conceal their content before checking lock
   state, unloading, entering the back-forward cache or becoming hidden,
   and MUST verify that the server session is unlocked before revealing
   a restored document. A history load from a visible, unconcealed
   document whose idle timer has not elapsed MUST NOT conceal; the
   server's answer to that load enforces the lock. Back-forward cache
   restores still verify. Protected HTML starts concealed until the
   script confirms unlock; both the concealment guard and `/unlock`
   include a plain HTML sign-out form (`/unlock/leave`).
4. Concealment MUST apply only under `@media (scripting: enabled)`,
   since only a script can reveal. A browser without JavaScript shows
   the page the server answered: every protected request of a locked
   session is answered with `/unlock`, protected pages carry
   `Cache-Control: no-store`, and an open document or one restored from
   the back-forward cache stays as it was. Such a browser cannot unlock
   (WebAuthn needs script) and MAY only sign out.
5. Tabs coordinate via Server-Sent Events (`/events`),
   `BroadcastChannel` and a `localStorage` fallback, keyed by the public
   session ID and a fresh lock marker—never the cookie secret or account
   data.
6. Only a completed WebAuthn assertion clears the lock marker and
   reloads the full document. When a single tab's idle timer elapses, it
   probes the server rather than locking unilaterally if another tab was
   active.
7. Opening `/unlock` in a visible tab prompts for the passkey once
   automatically; dialog dismissal or focus changes MUST NOT trigger
   repeated prompts, leaving the manual retry button active.
8. Passkey registration requires an explicit click without a typed label
   and names the credential after the browser and OS from `User-Agent`.

## Consequences

- Locking one tab conceals all other tabs of the same browser session,
  even when SSE or `BroadcastChannel` is unavailable.
- A browser that runs script but cannot load `session-lock.js` stays
  concealed; one without JavaScript is guarded by the server alone, and
  its open tabs are not concealed when the session locks.
- One pending WebAuthn challenge is held per session; concurrent
  ceremonies in two tabs of the same session require a retry on the
  earlier tab.

## References

- WebAuthn Level 2; ADR 13, ADR 19, ADR 25, ADR 29
- `lock.go`, `web/lock.go`, `web/middleware.go`, `web/live.go`,
  `auth/web/lock.go`, `settings/web/passkeys.go`,
  `templates/assets/helpers.js`, `templates/assets/session-lock.js`,
  `templates/partials/nav.css`; Media Queries Level 5 (`scripting`)
