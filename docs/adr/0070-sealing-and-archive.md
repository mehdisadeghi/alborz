# ADR 70: Sealing, keyed names and the account archive

Status: accepted, amended by ADR 72 (format byte 2 for values sealed
under an account or collection key) and ADR 97 (60-day remembered
session lifetime). Amends ADR 5, ADR 26, ADR 27, ADR 51, ADR 59 and
ADR 65.

## Context

All persistent data on disk must be authenticated and encrypted with
modern AEAD primitives, database keys derived from addresses or URLs
must not leak those identifiers to offline inspection, and readers need
a portable export/import archive for data stored on the instance.

## Decision

### Sealing and keyed names

1. Every sealed value MUST use AES-256-GCM with a random 96-bit nonce
   (`cipher.NewGCMWithRandomNonce`), prefixed by a format byte bound as
   additional authenticated data: format `1` for instance-derived keys,
   format `2` for account or collection keys (ADR 72).
2. Unrecognised format bytes return `seal.ErrFormat`; decryption
   failures return `seal.ErrOpen`.
3. `-login-key` is 32 bytes in base64. Purpose-specific keys are derived
   via HKDF-SHA256 with info `"alborz seal " + purpose`
   (`alborz.sessions`, `alborz.settings`, `alborz.pim`, `alborz.keys`,
   `alborz.push`, `alborz.mail.send`, `alborz.setup`, `dav.mirror`,
   `alborz.keycheck`). Nothing is sealed directly under `-login-key`.
4. Every key segment derived from an address, collection or object name,
   UID, publication secret, endpoint or URL MUST be
   `hex(HMAC-SHA256(namesKey, value)[:16])`, where `namesKey` is derived
   via HKDF-SHA256 with info `"alborz names"` (`seal.Names.Of`).

| Kept | File | Key | Key parts |
|---|---|---|---|
| Sessions | `alborz.db` | `alborz.sessions` | session ID |
| Remembered password | session record | SHA-256 of cookie secret (valid 60 days from last activity, ADR 97) | - |
| Settings without METADATA | `alborz.db` | `alborz.settings` (or account key, ADR 72) | keyed account |
| Own collections, objects, shares, UIDs, publications, ctag | `alborz.db` | `alborz.pim` (or collection key, ADR 72) | all keyed |
| DAV mirror | `<cache>/caldav.db`, `carddav.db` | `dav.mirror` (or account key, ADR 72) | all keyed |
| HTTP password (ADR 5) | settings | random key wrapped under Argon2id of mail password (or account key, ADR 72) | - |
| Key check | each `.db` file | `alborz.keycheck` | - |

5. Unreadable sealed records in `pim/store` fail the transaction so
   nothing overwrites them (ADR 59).

### Account archive

6. `/settings/data/export` streams an unencrypted `.tar.gz` archive
   (`alborz-<account>-<date>.tar.gz`):

       alborz.json                       {Format: 1, Account, Exported, Build}
       settings/<entry>.json             each settings entry, opened
       collections/<id>/collection.json  collection, share and object metadata
       collections/<id>/<object>         raw object data (.ics, .vcf)

   Mail, remote server data, the HTTP password and the `key` entry
   (ADR 72) are excluded.
7. `/settings/data` reads the archive in memory (capped at 256 MiB
   uncompressed), verifies `alborz.json`, and restores each settings
   entry and collection for the importing account without deleting
   unspecified records (ADR 27).
8. A publication secret already in use by another collection on this
   instance is dropped on import and noted in a warning notice.

## Consequences

- Sealing under `-login-key` protects database and cache files at rest;
  per-account encryption (ADR 72) additionally protects them from the
  instance key.

## References

- RFC 5869 (HKDF); ADR 5, ADR 26, ADR 27, ADR 51, ADR 59, ADR 65,
  ADR 72, ADR 97
- `internal/seal/seal.go`, `records.go`, `secret.go`, `web/session.go`,
  `pim/store/store.go`, `pim/dav/mirror.go`, `archive.go`,
  `pim/store/archive.go`, `settings/web/data.go`
