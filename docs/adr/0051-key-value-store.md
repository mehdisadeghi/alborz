# ADR 51: Key-value interface to the data file

Status: accepted, amended by ADR 70 (keyed names and sealed values) and
ADR 72 (per-account and per-collection keys).

## Context

`alborz.db` holds all persistent state owned by alborz (ADR 26). Calling
`bbolt` directly from feature packages couples every data owner to one
storage engine.

## Decision

1. Package `internal/kv` MUST be the sole interface to `alborz.db`:

   ```go
   type DB interface { View(func(Tx) error) error; Update(func(Tx) error) error; Close() error }
   type Tx interface { Get(k string) []byte; Put(k string, v []byte) error; Delete(k string) error; Scan(prefix string, fn func(k string, v []byte) error) error }
   ```

2. Keys are flat strings prefixed by their owner:

   | Owner | Keys |
   |---|---|
   | `data.go` | `alborz.meta` (plain JSON) |
   | `records.go` | `alborz.keycheck`, `alborz.sessions/<session id>`, `alborz.settings/<account>`, `alborz.settings/key/<account>` |
   | `keys.go` | `alborz.keys/<account>` |
   | `setup.go` | `alborz.setup/<key>` |
   | `push.go` | `alborz.push/vapid`, `alborz.push/devices/<account>/<endpoint>`, `alborz.push/sent/<endpoint>/<item>` |
   | `mail` | `alborz.mail.send/<claim>` |
   | `pim/store` | `alborz.pim/collections/<owner>/<id>`, `alborz.pim/objects/<owner>/<id>/<name>`, `alborz.pim/shares/<owner>/<id>\x00<to>`, `alborz.pim/received/<to>\x00<owner>/<id>`, `alborz.pim/uids/<owner>/<id>\x00<uid>`, `alborz.pim/public/<secret>`, `alborz.pim/dropped/<owner>/<id>`, `alborz.pim/keys/<scope>`, `alborz.pim/ctag` |

   Identifier segments other than random session IDs are keyed names,
   and values other than `alborz.meta` are sealed (ADR 70, ADR 72).
3. Each owner registers its prefix and format version in `alborz.meta`
   (`Server.Data`).
4. Typed stores wrap `kv.DB` and expose domain types only; callers MUST
   NOT manipulate raw keys or transactions.
5. `internal/kv/bolt` is the only importer of `bbolt` and stores all
   keys in a single bucket. A lock timeout returns `kv.ErrLocked`.
6. Mutations during `Scan` are forbidden; `kv.DeleteUnder` and
   `kv.DeleteExpired` collect keys before deleting.

## Consequences

- Replacing the storage engine requires only a `kv.DB` implementation
  and a key scan/copy.

## References

- ADR 26, ADR 70, ADR 72, ADR 74, ADR 87, ADR 98
- `internal/kv/kv.go`, `internal/kv/bolt/bolt.go`, `data.go`,
  `records.go`, `keys.go`, `push.go`, `setup.go`,
  `mail/send_claim.go`, `pim/store/store.go`
