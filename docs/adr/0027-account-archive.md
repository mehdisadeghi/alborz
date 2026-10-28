# ADR 27: Account data export and import

Status: accepted, amended by ADR 70 (archive layout: a `.tar.gz` of
plain files; HTTP password excluded; an import never takes over a
publication address serving another collection).

## Context

For collections kept here (ADR 24) and for settings, alborz is the
source of truth. A reader moving instances needs that data out and back
in, readable by every later version (ADR 26).

## Decision

1. Settings > Your data exports one account's data
   (`/settings/data/export`) and imports it (`/settings/data`). Layout
   and limits are ADR 70's.
2. The archive holds what the account owns: its collections with their
   objects and shares and publication secret, and its settings entries,
   wherever they are kept (METADATA or `alborz.db`).
3. It omits mail, data on other servers, collections shared with the
   account, the HTTP password, and everything bound to a browser
   (sessions, passkeys).
4. Records go as stored, opened: unknown fields travel. ADR 26 rules
   1-5 apply to the archive. The manifest carries `Format` 1; import
   MUST read every format ever written.
5. Import restores, it does not synchronise. Each record replaces the
   stored record of the same key; nothing the archive does not name is
   deleted, so a repeated import changes nothing further.
6. The owner is the importing account, whatever the archive says.
   Shares keep their state. The ctag moves so DAV clients refetch.
7. Settings are read from the store per request (ADR 52), so the next
   request sees an import; a session keeps its profile until the
   reader changes it.

## Consequences

- The archive is unencrypted, downloaded by the reader; alborz writes
  it nowhere.
- A replaced record keeps the archive's unknown fields and loses the
  stored record's: an older version importing over a newer one's
  record drops what only the newer knew. This is the one exception to
  ADR 26 rule 3.

## References

- ADR 24, ADR 26, ADR 52, ADR 70
- `archive.go` (`Server.Export`, `Server.Import`, `ArchivePart`,
  `exportSettings`), `pim/store/archive.go` (`Export`, `Import`,
  `restore`), `settings/web/data.go`, `settings/web/data_test.go`
