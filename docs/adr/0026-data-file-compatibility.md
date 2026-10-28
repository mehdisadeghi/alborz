# ADR 26: One data file, compatible across versions

Status: accepted, amended by ADR 51 (owner-prefixed keys replace
buckets) and ADR 70 (every record sealed; `alborz.sessions`,
`alborz.settings` and `alborz.pim` moved to format 2 without a
fallback reader).

## Context

Several versions use one data file in sequence (upgrade, rollback, a
build run on a copy), and concurrently use the settings kept in
METADATA on the mail server. Terms follow Kleppmann, *Designing
Data-Intensive Applications*, ch. 4: backward compatibility (new code
reads old data) and forward compatibility (old code reads new data).
The forward failure is an old version rewriting a record without the
fields it does not know.

## Decision

### One file

1. All data alborz owns MUST be in `alborz.db` in the data directory
   (`-data-dir`, default the working directory), under keys prefixed by
   their owner: `alborz.sessions`, `alborz.settings`, `alborz.pim`
   (ADR 51). A missing data directory is fatal; alborz does not create
   it.
2. The server opens the file once and hands it to every owner. The
   bbolt lock (waited on for 5 s) stops a second alborz on the same
   directory.

### Records

3. Records are JSON objects. Unknown numbers are carried as
   `json.Number`. These rules bind the file and the settings in
   METADATA alike:
   1. A field is added as optional; its zero value MUST mean what the
      record meant without it.
   2. A reader MUST ignore unknown fields.
   3. A writer MUST keep unknown fields: every write merges into the
      stored record at every level of nested objects
      (`record.Keep`). Elements of a list are the writer's; a field
      that must survive older versions goes on an object.
   4. A field's name, type and meaning MUST NOT change.
   5. A field is removed by no longer reading it, and written for as
      long as older versions read it. Its name MUST NOT be reused.
   6. A change rules 1-5 cannot express uses a new owner prefix with a
      version (`alborz.pim.2`), written beside the old one while older
      versions are supported.
4. The `alborz.meta` record holds the instance name, the format per
   owner, and the last 8 builds that opened the file. A version that
   finds a newer format than it knows logs it and still reads and
   writes under rules 1-5.
5. There is no migration on open.

### Derived caches

6. The DAV mirror (`caldav.db`, `carddav.db`) lives in the cache
   directory, never in `alborz.db` (ADR 53, ADR 54). It is derived
   data: no compatibility rules apply, and deleting it is always safe.
   A cache file sealed under another login key is refused at start,
   never emptied (ADR 59).

### General rules

7. Retried writes SHOULD be idempotent (PUT with If-Match, moves by
   UID).
8. Servers own mail, calendars and contacts; alborz's caches of them
   are dropped, never repaired.
9. Changes are ordered by versions (ETag, ctag, UIDVALIDITY, MODSEQ),
   never by clocks.
10. Every network wait is bounded by a timeout.
11. Concurrent edits are detected with If-Match, not prevented.

## Consequences

- Every write costs a read of the stored record in the same
  transaction.
- Lists of objects are the stated gap of rule 3.
- METADATA has no compare-and-set: a write between another version's
  read and write is lost.
- The account archive (ADR 27, ADR 70) is the archival dump; its
  `Format` 1 is read by every later version.

## References

- RFC 5464; ADR 27, ADR 51, ADR 53, ADR 54, ADR 59, ADR 70
- `data.go` (`openData`, `dataMeta`, `Server.Data`, `noteFormats`),
  `records.go` (`sessionsOwner`, `settingsOwner`, `openTimeout`,
  `CheckKey`), `internal/record/record.go` (`Keep`), `store.go`
  (`imapMetadataStore.Put`)
