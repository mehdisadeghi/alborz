# ADR 56: Package placement

Status: accepted.

## Context

The root package and `mail` held code that needs neither: calendar
arithmetic, text direction and language detection, a per-user memo,
the search language, and the sign-in and lock pages.

## Decision

A unit of code MUST be placed by its imports and its users:

1. Code that depends on no alborz feature goes under `internal/`, is
   tested alone, and is not importable outside the module. Examples:
   `internal/calsys` (Gregorian and Solar Hijri day counting),
   `internal/lang` (text direction, language of a text),
   `internal/memo` (one derived value per user), `internal/kv`,
   `internal/record`, `internal/pgp`.
2. Code that belongs to one feature and needs little of it is a
   subpackage of that feature. `mail/imapsearch` maps the shared
   `search` grammar (ADR 21, ADR 69) into IMAP SEARCH criteria and
   imports only go-imap.
3. Pages go with what they serve. Sign-in, sign-out, lock and unlock
   are served by `auth/web`, mounted in `cmd/alborz/app` beside the
   other features. The session and the lock of a request are in `web`
   (ADR 66); logins and the persisted session and lock records stay in
   the root package.
4. A helper lives with its only users: `pim.AppendNote` (dated notes on
   events and contacts) is in `pim`.
5. A move is its own commit and carries no edits.

## Consequences

- `mail` serves only mail pages.
- Open: methods on `Context`, `Server`, `Login` and `Session` that
  belong to one feature; an IMAP layer separate from `mail`.

## References

- ADR 21, ADR 66, ADR 69
- `internal/`, `search/`, `mail/imapsearch/`, `auth/web/`,
  `cmd/alborz/app/routes.go` (`Mount`), `web/session.go`, `web/lock.go`,
  `records.go`, `lock.go`, `pim/note.go`
