# ADR 88: Moving and copying DAV objects

Status: accepted.

## Context

Events, tasks, journal entries and contacts need the same Copy to and Move to
actions. Collections may belong to different accounts or servers, so a move
cannot always be atomic.

## Decision

- Object menus offer both actions. Read-only shares and subscribed calendars
  remain valid copy sources; only writable collections may be move sources or
  destinations. Calendar destinations must support the object's component.
- Selectable DAV lists also offer bulk Copy to. Selected references include
  their accounts and are deduplicated, including recurring event occurrences
  that refer to one resource. A partial failure reports completed copies and
  retains only unfinished references for a retry.
- Copy creates a new UID. Calendar copies retain the existing behavior of
  removing scheduling properties and recording their origin. Contact copies
  retain the complete card with a new UID and revision.
- Move retains the object's identity and data. Within one account and DAV
  source, use conditional native MOVE without overwriting the destination.
  Fall back only when the server explicitly does not implement MOVE.
- Across sources or accounts, create the destination conditionally, then
  conditionally delete the version read from the source. Both versions must
  have strong ETags. Missing versions cause a refusal, preserving the source.
- For objects with scheduling participants, refuse the fallback if either
  server schedules automatically or its capabilities cannot be established.
  Ordinary PUT/DELETE must not send invitations or cancel a meeting merely
  because its owner wanted to move it. Native MOVE remains available.
- Roll back a destination only after an explicit source-deletion refusal and
  fresh confirmation that the source exists. Rollback itself is conditional
  on the created destination's ETag, preserving concurrent destination edits.
- An uncertain deletion retains the destination. The error asks the reader to
  check both collections before retrying. A lost response must not cause the
  last remaining copy to be deleted.
- Native moves invalidate both mirrored collections and prevent a sync begun
  before the move from restoring their stale contents.

## Consequences

Moves between independent servers can leave two copies after failures. They
do not overwrite collisions or discard the destination to conceal uncertainty.
Servers without native MOVE may refuse same-principal calendar transfers when
their UID uniqueness or scheduling rules prevent the fallback operation.

The interface checks permissions, and the handlers enforce them independently.
The existing bulk task move uses the same transfer rules.
