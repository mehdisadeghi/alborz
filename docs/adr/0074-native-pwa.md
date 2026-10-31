# ADR 74: Notifications, badge, share target and mailto

Status: accepted, amended by ADR 90 (mail preview, calendar event title
and notification actions), ADR 97 (60-day remembered inactivity
window) and ADR 102 (merged settings lists). Amends ADR 14, ADR 19 and
ADR 72.

## Context

An installed web app needs to notify the reader of new mail and upcoming
events, show an unread count on its icon, receive shared content, and
register as a `mailto:` and `webcal:` handler. Web Push (RFC 8030)
delivers encrypted payloads (RFC 8291, RFC 8188) authenticated with
VAPID (RFC 8292) through the browser vendor's push service. Because an
account's password and encryption key live in memory only while a login
is alive (ADR 72), checking mail and calendars while the app is closed
requires keeping that login alive while a push device is active.

## Decision

### Web Push

1. `internal/webpush` MUST implement RFC 8291 encryption (single record
   of up to 4096 octets, payload up to 3992 octets) and RFC 8292 VAPID
   (ES256 JWT, `aud` set to the endpoint origin, `exp` 12 hours ahead)
   using the standard library only.
2. The VAPID key is the instance's push identity, created at first start
   and stored at `alborz.push/vapid` under the `alborz.push` purpose key
   (ADR 70). A key that fails to open MUST stop startup; it MUST NOT be
   regenerated while stored.
3. The VAPID `sub` is `-public-url`, else the origin from which the
   device enabled notifications.
4. Every push MUST be sent through `Outgoing.Remote` (public addresses,
   HTTPS at every hop) under a 10 s timeout (`pushTimeout`) in its own
   goroutine.

### Devices

5. A device record is one browser's subscription for one account at
   `alborz.push/devices/<keyed account>/<keyed endpoint>`, sealed under
   the account's `alborz.push` key (instance key when encryption is off,
   account key when on, ADR 72). It stores endpoint, `p256dh`, `auth`,
   session ID, User-Agent, language, timezone, origin and creation time.
6. `/settings/notifications` (`settings.notifications`) lists, for
   each account in view (ADR 102), the current browser with an on/off
   toggle and then the account's other browsers, one row each. Enabling requires an
   `https` endpoint, a valid P-256 `p256dh` point and a 16-octet `auth`
   secret. Re-enabling from the same browser replaces the record with
   the current session, language and timezone.
7. Pushes MUST be sent only to devices whose session still holds the
   account (live or remembered within the inactivity limit, ADR 97).
   Signing a browser out of an account MUST delete that browser's device
   record for the account.
8. When a push service answers 404 or 410 (RFC 8030 7.3), the device
   record MUST be deleted. Other delivery errors are logged and the
   record is kept.

### At most once

9. Each push identifies its item: new mail by account, INBOX
   `UIDVALIDITY` and newest UID; a reminder by event UID, occurrence
   start and alarm time. Before sending, the item MUST be claimed in one
   transaction at `alborz.push/sent/<keyed endpoint>/<keyed item>` under
   the instance's `alborz.push` key. An already-claimed item MUST NOT be
   sent again.
10. Claims older than 48 hours (`pushSentKeep`) are swept when the
    endpoint's next claim is recorded.
11. A claim is recorded before sending and kept if delivery fails:
    delivery is at most once.

### Payload and triggers

12. The payload is JSON rendered in the device's language and timezone.
    Content disclosure and actions follow ADR 90.
13. The settings page MUST state what the push service sees (delivery
    time and payload size, not content) and what the notification shows.
14. `TTL` is 1 hour (`pushTTL`). Mail pushes set `Topic` to the
    account's keyed name so undelivered mail notifications coalesce;
    reminders set `Urgency: high`.
15. New mail: the watcher (ADR 45) pushes when INBOX grows (`EXISTS`
    above the held count, or a larger `UIDNEXT` on sweep). Mail filed
    into other folders does not push.
16. Event reminders: every minute (`reminderTick`), for each account
    with a live, unlocked login and at least one device, alborz queries
    event calendars within 24 hours of the tick (`reminderReach`),
    expands recurrences, and pushes each `DISPLAY` or `AUDIO` alarm
    whose trigger falls between the previous tick and this one. Floating
    times use the profile's timezone. `REPEAT` and `EMAIL` alarms and
    triggers beyond `reminderReach` are ignored.
17. A login needed by an active device whose session is still valid MUST
    be kept past `LoginIdle` (30 minutes). When the last device for an
    account is turned off or its session expires or signs out, the login
    is rechecked and closed. Server restart ends all live logins until a
    browser returns.
18. The service worker displays notifications via `showNotification`,
    sets the app badge on mail pushes (`setAppBadge()`), and opens or
    focuses the target URL on click (ADR 90).
19. On `pushsubscriptionchange`, the service worker posts the old and
    new endpoints to `settings.notifications.create`, replacing existing
    device records for the session's accounts without adding new ones.
20. Pages with the mail rail carry the session's total unread INBOX
    count (`data-unread`) and update the app badge on load and rail
    refresh.
21. The web app manifest declares `share_target` (`POST`
    `multipart/form-data` to `mail.share` with `title`, `text`, `url`,
    `files`). The handler reads uploads into sealed spools (ADR 65)
    under the attachment limit and redirects to compose.
22. The manifest declares `protocol_handlers` for `mailto` and `webcal`.

## Consequences

- Delivery relies on the browser vendor's push service, which observes
  delivery timing and payload size.
- An enabled device keeps its account's login, password and encryption
  key in server memory while its session remains valid (up to 60 days
  with remember-me, ADR 97).
- Reminders due while alborz is stopped, or written by another client
  within the mirror's 2-minute CalDAV poll window (ADR 53), may be
  missed.

## References

- RFC 8030, RFC 8188, RFC 8291, RFC 8292; RFC 5545 3.6.6, 3.8.6.3
- W3C Push API, Notifications API, Badging API, Web Share Target
- ADR 14, ADR 19, ADR 45, ADR 53, ADR 65, ADR 70, ADR 72, ADR 90, ADR 97,
  ADR 102
- `internal/webpush`, `push.go` (`Pushes`), `web/push.go` (`Push`,
  `keepLogin`), `web/serviceworker.go`, `web/routes.go`,
  `settings/web/notifications.go`, `templates/assets/push.js`,
  `mail/watch.go`, `pim/calendar/reminders.go`,
  `pim/calendar/web/reminders.go`, `login.go` (`reap`, `Recheck`),
  `mail/web/sidebar.go` (`Unread`), `mail/web/share.go`
