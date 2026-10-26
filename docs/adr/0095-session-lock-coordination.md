# ADR 95: Session lock coordination

Status: accepted.

## Decision

- Compact header and account-menu actions lock the current browser session
  without ending its accounts or replacing its cookie. The header action appears
  only after a passkey is configured in Settings.
- Keep authorization on the server: protected routes and streams check the
  session lock. A minimal, uncached state probe reports only the public session
  identifier and lock flags. Background probes do not extend the idle period.
- Conceal private pages before checking state, leaving, entering the history
  cache or becoming hidden. Verify the same server session before revealing a
  restored page. Protected HTML starts concealed even if its script fails.
- Passkeys are an optional JavaScript enhancement to the HTML baseline. Both
  the concealment guard and unlock page retain a native sign-out form, so a
  browser without scripts can discard its protected session and sign in with
  passwords again. Missing scripts never authorize revealing private content.
- Coordinate all tabs through server events, BroadcastChannel and a storage
  fallback. Scope messages to the public session identifier, never the cookie
  secret or account content. Use a fresh marker for each confirmed lock.
- Only a successful WebAuthn finish clears the shared lock marker. An individual
  tab's idle timer asks the server rather than assuming the whole session is idle.
  Failed or ambiguous lock requests remain concealed with a retry action.
- Successful authentication transitions replace the whole document, including
  its session-scoped head metadata. First passkey registration also informs tabs
  that were opened before protection was enabled.

## Verification

HTTP tests check a manual lock from an unlocked session, independent clients
sharing its cookie, account preservation, unasked state probes and stream races.
Browser-handler tests cover storage fallback, restored pages, stale responses,
cancelled assertions and repeated locks after a lost unlock response.

A real browser with a virtual WebAuthn authenticator additionally verifies a
pre-registration compose tab with SSE and BroadcastChannel disabled, concealment
before stalled unlock navigation, new tabs, Back/history, cancellation and a
successful unlock without signing in again or changing the session cookie.
