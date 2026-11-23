# ADR 107: Device passwords

Status: accepted, amended by ADR 109. Amends ADR 24, ADR 30, ADR 51, ADR 70 and ADR 72.

## Context

CLI and desktop DAV clients (vdirsyncer, khal, todoman, Thunderbird,
DAVx5) keep their password in a file or keychain. With only the mail
password accepted (ADR 24), every such client holds the key to the
mailbox and, under encryption (ADR 72), to the account's data.

## Decision

### Passwords

1. An account MAY have device passwords, listed at `/settings/devices`
   with name, access (read and write, or read only), scope (ADR 109),
   creation and last use, and revoked there. Each has a page of its own
   (ADR 109).
2. A device password MUST be `rand.Text()` (130 random bits), shown once
   on creation beside the DAV address and the username, and MUST NOT be
   stored. Its SHA-256 hash is stored and compared in constant time.
3. Under encryption, a device password MUST keep the account key's ID
   and, unless it is scoped, wrap the account key under HKDF-SHA256 of
   the password (info `alborz device password`). A scoped one wraps
   the keys of its collections instead (ADR 109).
4. Last use MUST be written at most once an hour (`deviceTouch`).

### Storage

5. Each account's device passwords and its device-passwords-only
   setting are one record at `alborz.devices/<keyed account>`, sealed
   under the instance's `alborz.devices` key, since a DAV request reads
   it before the account key is open. It is not part of the account
   archive (ADR 70).
6. Turning encryption on or off, or starting over, MUST delete the
   device passwords not made under the account key now in force, by
   the ID they keep (rule 3), and with encryption off those made under
   any (`Server.Sealers`); their secrets are unknown, so they cannot
   wrap another key. A mail password change MUST leave them intact.

### DAV authentication

7. A Basic auth password MUST be checked against the account's device
   passwords first, with no IMAP login. Without a match, the mail
   password is checked as in ADR 24, unless the account takes device
   passwords only, when the request MUST be refused.
8. Device password checks MUST share the refusal count of ADR 30: a
   match or a fallback to the mail password releases the try; a refusal
   under device passwords only counts. Refusals are cached as in ADR 30;
   acceptances by device password MUST NOT be cached, so a revoked one
   is refused at the next request.
9. A matching device password that is not scoped MUST open a locked
   account key with its wrap and hold it for `davPasswordLife` past
   the request (`Ring.Lend`), as the mail password does (ADR 72). A
   scoped one MUST NOT open or hold it (ADR 109).
10. A read-only device password MUST be refused with 403 for every
    method other than GET, HEAD, OPTIONS, PROPFIND and REPORT, and
    every collection it reaches MUST report no write privilege
    (`DAV:current-user-privilege-set`, RFC 3744 5.4).

## Consequences

- An unscoped device password opens the account key, so it is as
  strong a recovery path as the recovery key; a later sign-in with a
  reset mail password rewraps the key it holds.
- A revoked unscoped device password's key lease runs out within
  `davPasswordLife`.
- A refusal cached under device passwords only keeps refusing that
  mail password for `davPasswordLife` after the setting is turned off.
- Device names and last use are under the instance's key, as sessions
  are, not the account key.

## References

- RFC 7617 (Basic), RFC 5869 (HKDF), RFC 4918 (WebDAV methods),
  RFC 3744 (WebDAV access control)
- ADR 24, ADR 30, ADR 51, ADR 59, ADR 70, ADR 72, ADR 102, ADR 109
- `devices.go` (`Devices`, `DevicePassword`, `CreateDevicePassword`,
  `CheckDevicePassword`, `deviceTouch`), `internal/keyring/keyring.go`
  (`Lend`), `pim/web/owndav.go` (`davPasswords.authenticate`,
  `readMethods`), `pim/store/paths.go` (`ReadOnly`),
  `settings/web/devices.go`,
  `templates/pages/settings/devices.html`, `device-create.html`,
  `device.html`
