# ADR 20: Own DAV writes applied to the mirror

Status: accepted, amended by ADR 53 (writes update the synced collection
copy) and ADR 88 (native `MOVE` and cross-source transfer rules).

## Context

Evicting a mirrored DAV collection on every write forces the next page
load to wait for a full collection sync to show a single change.

## Decision

1. A confirmed `PUT` that returns a strong ETag MUST store the sent body
   under that ETag in the mirrored copy of the collection (RFC 4791
   5.3.4, RFC 6352 6.3.2.3). A confirmed `DELETE` MUST remove the object
   from the mirrored copy.
2. A `PUT` answered without an ETag, a native `MOVE` (ADR 88), or a
   refused `PUT` or `DELETE` MUST mark the mirrored collection stale so
   the next read syncs it (ADR 53). On HTTP 412, the collection is
   synced before returning the refusal.
3. A confirmed collection-level write (`MKCOL`, `MKCALENDAR`,
   `PROPPATCH`, or a path ending in `/`) drops the collection's mirrored
   copy.
4. Each collection tracks a write counter; a background sync that
   started before an own write MUST NOT overwrite the collection.
5. Only collections already held in the mirror are patched.
6. Object editors (event, task, journal entry, contact) send the ETag
   their form was rendered with as `If-Match` (RFC 9110 13.1.1). A 412
   re-renders the form with the user's input, `form.changedelsewhere`
   and a reload link. Single-property actions (star, colour, completion,
   note, group membership) retry once against a fresh read on 412.

## Consequences

- The page following a write renders immediately from the updated mirror
  copy.

## References

- RFC 4791 5.3.4, RFC 6352 6.3.2.3, RFC 9110 13.1.1
- ADR 6, ADR 53, ADR 88
- `pim/dav/mirror.go` (`tripper.write`, `Mirror.wrote`, `Mirror.sync`),
  `pim/web/act.go` (`Drawn`, `Unsaved`)
