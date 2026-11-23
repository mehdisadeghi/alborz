# ADR 109: Scoped device passwords

Status: accepted. Amends ADR 24, ADR 27, ADR 72 and ADR 107.

## Context

A device password (ADR 107) reaches every collection of its account,
for reading or for reading and writing, and under encryption (ADR 72)
wraps the account key. A client that needs one calendar, such as a wall
display or a script, holds all of them and the key to the rest.

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

5. Under encryption (ADR 72) a scoped device password MUST NOT wrap
   the account key. It MUST keep the account key's ID (ADR 107 rule 3)
   and, in `Wraps` of its record, wrap the key of each collection of
   its scope that has one (ADR 72 rule 6) under HKDF-SHA256 of the
   password, with info `alborz device password collection ` followed
   by the collection's name in the scope (rule 3). With encryption off
   it wraps nothing.
6. A request authenticated by a scoped device password made under
   encryption MUST be served with the keys it wraps alone
   (`Store.Holding`). It MUST NOT open or hold the account key (ADR 107
   rule 9), and no key pair on the instance opens a collection for it.
   A collection whose key it does not wrap, or wraps as it was before
   the key was replaced, is absent to it (ADR 72 rule 7). A collection
   without a key and a published one (ADR 32) are read under the
   instance's key, as for any request.
7. A scoped device password is deleted with the account key it was
   made under (ADR 107 rule 6), which is when the account's own
   collections get, lose or change their keys. When the owner of a
   collection shared with the account replaces its key, or gives it
   its first, the password is kept and the collection is absent to it
   from then on.

### DAV server

8. A request authenticated by a scoped device password MUST be served
   as its account with the scope as a limit (`store.Limited`), which
   the store's backends apply to every collection they reach or list.
9. Discovery MUST answer a scoped device password as any other: the
   well-known addresses (RFC 6764), the root, the principal and the
   home sets. A PROPFIND of a home set MUST list only the collections
   in scope.
10. Every method on a collection out of scope, or on an object in one,
    MUST be answered as the same request on a name no collection has.
    An href out of scope in a calendar-multiget or addressbook-multiget
    REPORT MUST be answered 404, whatever address the REPORT is sent to.
11. MKCOL and MKCALENDAR in a home set MUST be refused with 403, whether
    or not a collection has the name, and DELETE of a collection in
    scope MUST be refused with 403: a scoped device password makes no
    collection, deletes none of the account's and leaves none shared
    with it.
12. ADR 107 rule 10 holds before the scope is read: a read-only scoped
    device password is refused with 403 for every other method at every
    address. With write, PUT and DELETE of objects and PROPPATCH of a
    collection in scope are the account's (ADR 24 rule 7).
13. A collection shared with the account is in scope at its address in
    the account's home (`<id>~<owner>`) for as long as the share gives
    access. A share that ended answers as rule 10; given again, the
    collection is in scope again.

### Pages

14. `/settings/devices/create` MUST offer every collection or a choice
    among those the account sees, grouped by kind and named as the
    collection picker names them (`collection-name`), a subscribed
    calendar marked as one. Rows of a kind that read alike MUST each
    add the collection's ID, which its address to a DAV client holds.
    A choice of none, or of a collection the account does not see, MUST
    be refused with 422.
15. The form MUST show the collections only for a password of chosen
    ones (CSS `:has()`, no script); for a password of all of them they
    MUST be ignored when submitted.
16. `/settings/devices` states each password's scope after its access:
    every collection, or the count of each kind and the count of the
    scope's collections the account no longer sees, as no longer
    existing. A row's detail wraps, under the name on a phone
    (`.list-items.whole`), and its name links to the password's page.
17. `/settings/devices/{id}` states the password's access, creation
    and last use, and each collection of its scope with its kind,
    named as in the form. A collection the account no longer sees is
    listed as no longer existing.

## Consequences

- Under encryption a scoped device password, with the data file and
  `-login-key`, opens the collections of its scope and nothing else
  the account key protects (ADR 72): no other collection, setting or
  mirrored copy. A fault in the access checks (rule 8) reaches no
  collection whose key the password lacks.
- The boundary is the password's, not the server's: while the account
  is open on a running server, by a browser, the mail password or an
  unscoped device password, the server holds every key of the account.
  With encryption off, and for a collection without a key or
  published, the access checks are all there is.
- Reading and writing are one key: read-only access is the server's
  to keep. A wrap goes on opening a collection's records after its
  share ended, until the collection's key is replaced.
- A shared collection whose key was replaced stays listed in the
  password's scope on the pages; the client needs a new password.
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
- The scopes' keyed names, kinds and wraps are under the instance's
  key, as device names are (ADR 107); collection names and IDs are not
  in them.
- Sharing, publication and subscription are not DAV operations here,
  and scheduling, free-busy, WebDAV sync, COPY and MOVE are not served
  (ADR 24), so no device password reaches them. A published calendar
  (`/pub/`) is read without any password, in scope or not.

## References

- RFC 4791, RFC 6352, RFC 4918, RFC 6764
- RFC 5869 (HKDF)
- ADR 24, ADR 26, ADR 27, ADR 32, ADR 70, ADR 72, ADR 107
- `devices.go` (`DevicePassword`, `DeviceCollection`, `DeviceChoice`,
  `deviceRecord`, `CreateScopedDevicePassword`, `DevicePassword.Keys`,
  `prune`), `server.go` (`Server.DeviceChoices`,
  `Server.CollectionKey`), `pim/store/paths.go` (`Limited`,
  `LimitName`, `within`, `reachAt`, `visible`, `create`,
  `removeWithOptions`), `pim/store/sealed.go` (`Holding`, `Key`,
  `lentKey`), `pim/store/store.go` (`Collection.Made`),
  `pim/store/archive.go` (`restore`), `pim/web/owndav.go`
  (`davAccess`), `pim/web/devices.go` (`deviceChoices`, `limitOf`),
  `pim/web/scope_test.go`, `settings/web/devices_test.go`,
  `settings/web/devices.go` (`scopeRows`,
  `held`, `reaches`), `templates/partials/form.html`
  (`collection-name`), `templates/partials/settings.html`
  (`device-collection`), `templates/partials/settings.css`,
  `templates/assets/css/components/list-items.css`,
  `templates/pages/settings/device-create.html`, `devices.html`,
  `device.html`
