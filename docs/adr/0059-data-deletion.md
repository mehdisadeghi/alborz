# ADR 59: Stored data is deleted only on request

Status: accepted, amended by ADR 70 (keyed names and HKDF keycheck) and
ADR 97 (60-day remembered session inactivity).

## Context

A wrong login key at startup, a transient read error or an expired
session must never silently destroy stored user data or treat an
unreadable database as empty and overwrite it.

## Decision

1. alborz MUST delete persistent user data only on an explicit user or
   operator action.
2. Age, sign-out, failed reads, wrong keys and server setting changes
   MUST NOT delete on-disk records. In-memory state MAY be dropped at
   any time.
3. Every database file (`alborz.db`, `caldav.db`, `carddav.db`) MUST
   contain `alborz.keycheck`, sealed under the `alborz.keycheck` purpose
   key derived from `-login-key` (ADR 70). If `alborz.keycheck` fails to
   open or uses an unknown format, startup MUST fail naming the file.
4. An HTTP password envelope that the current mail password cannot open
   (`ErrSealed`, ADR 5) or account data whose encryption key is not open
   (ADR 72) MUST be kept untouched until the reader re-enters or resets
   it.
5. The DAV mirror drops a collection copy only when the upstream server
   answers 404 for that collection (ADR 53).
6. Remembered session records inactive past `credentialCookieLife`
   (60 days, ADR 97) and expired push/send claims (ADR 74, ADR 87) are
   swept as ephemeral security state.

## Consequences

- Rotating `-login-key` requires moving cache files aside and either
  keeping the previous `alborz.db` key or explicitly starting fresh.

## References

- ADR 5, ADR 26, ADR 28, ADR 53, ADR 70, ADR 72, ADR 97
- `records.go` (`keyCheckKey`, `CheckKey`), `server.go`
  (`credentialCookieLife`, `SessionRecords.Sweep`),
  `pim/dav/mirror.go`, `secret.go`
