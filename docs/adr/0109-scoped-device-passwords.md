# ADR 109: Scoped device passwords

Status: accepted. Amends ADR 24, ADR 27 and ADR 107.

## Context

A device password (ADR 107) reaches every collection of its account,
for reading or for reading and writing. A client that needs one
calendar, such as a wall display or a script, holds all of them.

## Decision

### Scope

1. A device password MAY be scoped: limited to a set of the collections
   kept here (ADR 24) that its account sees when the password is made,
   its own and those shared with it. An unscoped device password
   reaches every collection. The set is chosen on creation and MUST NOT
   change afterwards.
2. Access stays the password's own (ADR 107 rule 1), read and write or
   read only for the whole scope. A client that needs both takes two
   passwords.
3. A scope MUST name each collection by the keyed names of its owner
   and ID (ADR 70) and the ctag it was made with (`Collection.Made`,
   `Store.LimitName`), and keeps its kind (calendar, tasks, journal or
   addressbook) for the pages. A collection deleted and made again
   under its name, or imported from an archive (ADR 27), is made with
   another ctag and MUST NOT be in the scope.

### Storage

4. Scoped device passwords MUST be kept in the account's record (ADR
   107 rule 5) in a list of their own, `Scoped`, beside `Devices`. A
   version that does not know the list does not read them, so it
   refuses them, and keeps them through its writes (ADR 26 rule 3.3).
   A record without the list reads as unscoped passwords. There is no
   migration.

### Encryption

5. Under encryption (ADR 72) a scoped device password MUST wrap the
   account key as an unscoped one does (ADR 107 rule 3). The scope is
   enforced by the server alone; it is not a cryptographic boundary,
   and no key is narrowed to it.

### DAV server

6. A request authenticated by a scoped device password MUST be served
   as its account with the scope as a limit (`store.Limited`), which
   the store's backends apply to every collection they reach or list.
7. Discovery MUST answer a scoped device password as any other: the
   well-known addresses (RFC 6764), the root, the principal and the
   home sets. A PROPFIND of a home set MUST list only the collections
   in scope.
8. Every method on a collection out of scope, or on an object in one,
   MUST be answered as the same request on a name no collection has.
   An href out of scope in a calendar-multiget or addressbook-multiget
   REPORT MUST be answered 404, whatever address the REPORT is sent to.
9. MKCOL and MKCALENDAR in a home set MUST be refused with 403, whether
   or not a collection has the name, and DELETE of a collection in
   scope MUST be refused with 403: a scoped device password makes no
   collection, deletes none of the account's and leaves none shared
   with it.
10. ADR 107 rule 10 holds before the scope is read: a read-only scoped
    device password is refused with 403 for every other method at every
    address. With write, PUT and DELETE of objects and PROPPATCH of a
    collection in scope are the account's (ADR 24 rule 7).
11. A collection shared with the account is in scope at its address in
    the account's home (`<id>~<owner>`) for as long as the share gives
    access. A share that ended answers as rule 8; given again, the
    collection is in scope again.

### Pages

12. `/settings/devices/create` MUST offer every collection or a choice
    among those the account sees, grouped by kind and named as the
    collection picker names them (`collection-name`), a subscribed
    calendar marked as one. Rows of a kind that read alike MUST each
    add the collection's ID, which its address to a DAV client holds.
    A choice of none, or of a collection the account does not see, MUST
    be refused with 422.
13. The form MUST show the collections only for a password of chosen
    ones (CSS `:has()`, no script); for a password of all of them they
    MUST be ignored when submitted.
14. `/settings/devices` states each password's scope after its access:
    every collection, or the count of each kind and the count of the
    scope's collections the account no longer sees, as no longer
    existing. A row's detail wraps, under the name on a phone
    (`.list-items.whole`), and its name links to the password's page.
15. `/settings/devices/{id}` states the password's access, creation
    and last use, and each collection of its scope with its kind,
    named as in the form. A collection the account no longer sees is
    listed as no longer existing.

## Consequences

- Under encryption a scoped device password opens the whole account
  key and holds it open for `davPasswordLife`: whoever has the password
  and the data file reads every collection of the account.
- A scope does not follow a collection that is made again or imported;
  the client needs a new password. A scope keeps no names, so the pages
  name a collection that is gone by its kind alone, and a share that
  ended reads as a collection that no longer exists until it is given
  again.
- A browser without `:has()` shows the collections of a password for
  all of them; what is ticked there is ignored.
- A collection made before this ADR has `Made` zero. Deleted and made
  again under its name by an older version, it is in scope again.
- An older version neither lists nor revokes scoped device passwords.
  One that deletes a record it reads as empty deletes them with it,
  when it revokes the last unscoped one while the mail password is
  taken.
- The scopes' keyed names and kinds are under the instance's key, as
  device names are (ADR 107); collection names and IDs are not in them.
- Sharing, publication and subscription are not DAV operations here,
  and scheduling, free-busy, WebDAV sync, COPY and MOVE are not served
  (ADR 24), so no device password reaches them. A published calendar
  (`/pub/`) is read without any password, in scope or not.

## References

- RFC 4791, RFC 6352, RFC 4918, RFC 6764
- ADR 24, ADR 26, ADR 27, ADR 70, ADR 72, ADR 107
- `devices.go` (`DevicePassword`, `DeviceCollection`, `DeviceChoice`,
  `deviceRecord`, `CreateScopedDevicePassword`), `server.go`
  (`Server.DeviceChoices`), `pim/store/paths.go` (`Limited`,
  `LimitName`, `within`, `reachAt`, `visible`, `create`,
  `removeWithOptions`), `pim/store/store.go` (`Collection.Made`),
  `pim/store/archive.go` (`restore`), `pim/web/owndav.go`
  (`davAccess`), `pim/web/devices.go` (`deviceChoices`, `limitOf`),
  `pim/web/scope_test.go`, `settings/web/devices.go` (`scopeRows`,
  `held`, `reaches`), `templates/partials/form.html`
  (`collection-name`), `templates/partials/settings.html`
  (`device-collection`), `templates/partials/settings.css`,
  `templates/assets/css/components/list-items.css`,
  `templates/pages/settings/device-create.html`, `devices.html`,
  `device.html`
