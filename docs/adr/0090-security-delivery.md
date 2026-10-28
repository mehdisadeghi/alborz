# ADR 90: Mail notification actions and image loading

Status: accepted. Amends ADR 19, ADR 44 and ADR 74.

## Context

Push notifications that show only "New mail" require opening the app to
triage routine messages, while showing message previews on a locked
browser session would leak protected content. Separately, incoming HTML
mail uses percent-encoded `cid:` URLs, CSS `background-image` URLs and
legacy `http:` image links that need consistent sanitization and proxy
bounds.

## Decision

### Notifications

1. Mail push notifications link to the exact account, mailbox and UID
   via an authenticated notification-open route that checks
   `UIDVALIDITY` before redirecting. If `UIDVALIDITY` changed, it
   redirects to the folder without marking a reused UID read.
2. When the target device's browser session is unlocked, a mail push
   includes the `Subject` (capped at 100 characters) and a plain-text
   body excerpt (capped at 180 characters, fetched via `BODY.PEEK` with
   a 4 KiB header limit and 16 KiB body limit, leaving `\Seen`
   unchanged), and calendar reminders include the event title. When the
   session is locked, idle-locked or unrestored, pushes MUST include
   only the account address (and event time for reminders, ADR 74).
3. On unlocked sessions, mail notifications offer `Mark read` and
   `Delete` (move to Trash; refused if no Trash folder exists). Action
   requests use authenticated same-origin `POST`s with a custom header,
   pass the session lock middleware, and verify `UIDVALIDITY` on the
   same IMAP connection as the write. Failed or locked actions open the
   message for manual recovery without auto-replaying the mutation.

### Incoming images

4. Inline `cid:` URLs MUST be percent-decoded before matching MIME
   `Content-ID` headers.
5. CSS `url(...)` values on allowed style properties MUST pass the same
   consent and `/proxy` resolution as `<img src>` (ADR 44); CSS escapes,
   comments and unparseable `url()` values are stripped, and `@import`
   and `expression()` remain blocked.
6. Legacy `http://` and protocol-relative `//` remote image URLs are
   upgraded to `https://` when proxied. Outgoing image proxy requests
   MUST enforce public-address checks (`Outgoing.Guarded`) and `https`
   on every redirect, accept generic upstream MIME types only when byte
   sniffing confirms a supported image format, and cap buffered bodies
   at 5 MiB.

### Service worker

7. The service worker starts network fetch and offline-cache lookup
   concurrently; a live network response never waits for cache storage,
   and `Cache-Control: no-store` responses purge saved pages and never
   fall back to cached content (ADR 19, ADR 25).

## Consequences

- Unlocked browsers get actionable mail previews without marking mail
  read; locked sessions disclose no subject or body text.
- Remote images on hosts that do not serve HTTPS are not loaded.

## References

- RFC 2392 (`cid:`), RFC 8030, RFC 9051 (`BODY.PEEK`, `UIDVALIDITY`)
- ADR 19, ADR 25, ADR 44, ADR 74
- `mail/watch.go`, `mail/web/notification.go`, `mail/web/sanitize.go`,
  `mail/web/images.go`, `mail/web/proxy.go`, `web/serviceworker.go`,
  `pim/calendar/reminders.go`
