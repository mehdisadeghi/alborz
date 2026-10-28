# ADR 72: Encryption at rest per account

Status: accepted, amended by ADR 74 (push device records and logins kept
alive for notifications). Amends ADR 5, ADR 52, ADR 65, ADR 68 and
ADR 70.

## Context

Sealing data under keys derived from `-login-key` (ADR 70) leaves copies
of `alborz.db` and cache files readable by anyone holding `-login-key`.
An account needs optional per-account encryption under a key derived
from the mail password or a recovery key, while still supporting shared
collections, published calendars and DAV clients.

## Decision

### Keys

1. Per-account encryption is off by default and MAY be turned on or off
   at `/settings/encryption` (`settings.encryption`).
2. Enabling encryption (or starting over) generates a random 32-byte
   account key and a 130-bit recovery key (`rand.Text()`, displayed once
   for download). The account key is stored in the `key` settings entry
   (IMAP `METADATA` when supported, else
   `alborz.settings/key/<keyed account>` sealed under the instance key),
   wrapped under Argon2id of the mail password (ADR 5 parameters) and
   under HKDF-SHA256 of the recovery key, along with `Off` and
   `Retired` keys.
3. Purpose keys are derived from the account key via HKDF-SHA256
   (ADR 70). Values sealed under an account or collection key carry
   format byte `2`.
4. At first sign-in, each account gets an HPKE key pair
   (`MLKEM768X25519` with HKDF-SHA256 and AES-256-GCM, RFC 9180) at
   `alborz.keys/<keyed account>`: public key in the clear, private key
   sealed under the account key when encryption is on or the instance
   key when off.
5. The opened account key is held in memory while an account login is
   alive (including logins kept for push devices, ADR 74) and for
   5 minutes after a DAV client request whose password opened it
   (ADR 30). Otherwise the account is locked on the instance.

### Scope of keys

| Kept | Encryption off | Encryption on |
|---|---|---|
| Own collections, objects, shares, UIDs | `alborz.pim` | per-collection random key |
| Settings in `alborz.db` | `alborz.settings` | account key |
| Settings in IMAP `METADATA` | plain on mail server | plain on mail server (including wrapped `key` entry) |
| HTTP password (ADR 5) | mail-password envelope | account key |
| DAV mirror | `dav.mirror` | account key |
| Private HPKE key | `alborz.keys` | account key |
| Push devices (ADR 74) | `alborz.push` | account key |
| Sessions, passkeys, VAPID key, push claims, feed cache, indexes | instance / cookie secret | instance / cookie secret |

6. Each collection of an encrypted account has a random key stored at
   `alborz.pim/keys/<scope>`, wrapped via HPKE to the owner's and each
   sharee's public key (and sealed under the instance key while the
   collection is publicly published, ADR 32).
7. Values whose key is unavailable are skipped on read as absent, and
   writes that would overwrite them fail (ADR 59).

### Transitions and recovery

8. Toggling encryption requires the mail password (throttled per
   ADR 30), flips the keyring state, and reseals each collection,
   mirrored collection, settings record and push device (`Server.Sealers`)
   in per-item transactions, resuming idempotently on subsequent
   sign-ins if interrupted.
9. Changing the mail password through alborz (ADR 68) rewraps the
   account key and retired keys under the new password.
10. If the current mail password cannot open the account key (e.g. after
    an external password reset), the account signs in as locked:
    `/settings/encryption` accepts the recovery key to rewrap the
    account key under the new password, or allows an explicitly
    confirmed "Start over" that deletes unreadable local records and
    generates a new account key and recovery key.
11. While an account is locked, background DAV mirror sync and
    subscribed feed updates for that account pause.

## Consequences

- Offline copies of `alborz.db` and `caldav.db`/`carddav.db` for an
  encrypted account require the mail password or recovery key to open.
- Mail on the IMAP server, published calendars, and collections shared
  with unencrypted accounts remain readable by the server.

## References

- RFC 5869 (HKDF), RFC 9106 (Argon2), RFC 9180 (HPKE)
- ADR 5, ADR 26, ADR 30, ADR 52, ADR 59, ADR 65, ADR 68, ADR 70, ADR 74
- `internal/keyring/`, `keys.go`, `records.go`, `secret.go`, `store.go`,
  `pim/store/sealed.go`, `pim/dav/mirror.go`, `settings/web/encryption.go`
