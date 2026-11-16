# ADR 52: Settings storage and the reader's profile

Status: accepted, amended by ADR 72 and ADR 79. Amends ADR 4.

## Context

Account settings must survive restarts and sync across browsers, while
the reader's personal profile must apply across all signed-in accounts
and render on signed-out pages.

## Decision

### Account settings

1. Account settings MUST be stored in IMAP `METADATA` (RFC 5464) under
   `/private/vendor/alborz/` when the server advertises `METADATA` or
   `METADATA-SERVER`; otherwise in `alborz.db` under
   `alborz.settings/<keyed account>` (ADR 51), sealed under the
   account's key when encryption is on or the instance's key when off,
   with the `key` entry stored separately under the instance key
   (ADR 72).
2. `METADATA` reads are memoised for the duration of a single HTTP
   request, and held by the login for at most one minute. Every write
   through alborz (`Put`, `Update`, `Forget`) MUST drop what all logins
   of the account hold; `Update` and `Put` read the entry from the
   server, never from what is held.
3. Store write refusals re-render the form with an alert
   (`SaveRefusal`), never a 500 error page.

### Profile

4. `alborz.Profile` holds `StartPage` (default section on `/` and
   post-login redirect), `Reading` (messages per page, timezone, first
   weekday, prefer HTML, primary and secondary calendar), `Writing`
   (reply position, RTL direction part, cross-account signature pooling,
   client attribution), `Display` (theme, text size, script alignment,
   language, status bar segments, add-ons), `Place` (ADR 79) and
   `Toured` (acknowledged first desktop tour). Partial profile form
   submissions MUST NOT reset checkboxes that were not rendered on the
   submitted form. The per-tab scheme override (ADR 50), the session
   passkey lock (ADR 25) and the per-browser hidden rail
   (`localStorage` `rail`, applied as `data-rail` by `scheme.js`
   before first paint) are not part of the profile.
5. Saving the profile writes the `profile` entry to every signed-in
   account, failing only if every account refuses the write.
6. The session holds the active profile (persisted in
   `SessionRecord.Profile` for remembered sessions) and refreshes it
   from the first signed-in account on reader GET requests.
7. Display preferences (`alborz_theme`, `alborz_text`, `alborz_align`,
   `alborz_lang`) are mirrored in cookies for signed-out pages.

### Multi-account sign-in

8. Signing in the first account loads its stored profile, or saves the
   browser's cookie profile if the account has none.
9. Signing in an additional account with no profile or an identical
   profile adopts the active session profile.
10. Signing in an additional account whose stored profile differs
    (`Toured` excluded) raises a conflict notice listing the differing
    fields with two actions: take the account's profile
    (`/settings/profile/take`) or replace it with the active profile
    (`/settings/profile/discard`).

## Consequences

- No single account is designated as primary.
- A change another program or another alborz makes to the entries shows
  here up to a minute late.
- Forgetting an account's settings resets its profile copy while keeping
  its `key` entry (ADR 72).

## References

- RFC 5464; ADR 4, ADR 26, ADR 50, ADR 51, ADR 72, ADR 79
- `store.go`, `records.go`, `profile.go`, `web/profile.go`,
  `web/prefs.go`, `web/start.go`, `settings/web/profile.go`
