# ADR 28: Multiple DAV sources per account

Status: accepted, amended by ADR 31 (per-source transport and
credentials).

## Context

An account's calendars, tasks and contacts can be on several servers
at once: the domain's, one on the mail host, one the account names
(ADR 22), and alborz itself (ADR 24). Showing only the first found
hides the others, for example during a migration. Every page, link,
stored selection and mirror key names a collection or object by path
alone, and two servers can hold the same path.

## Decision

### Sources

1. Each account has sources with stable ids:
   - `own`: the server the account names (ADR 22);
   - `domain`: the operator's upstream or the domain's SRV record;
   - `host`: DAV at the mail host's `/.well-known/<service>` (RFC 6764
     5), HTTPS only at every redirect, when its host differs from the
     domain's. The server is where the redirects end, which MAY be
     another host;
   - `here`: this alborz.
2. Discovery MUST NOT replace one source by another; each found is
   listed. An `own` server equal to the domain's or mail host's is
   that source, not a second one.

### Paths

3. Every path alborz holds MUST begin with a marker naming the host of
   its server: `/@dav.example.org/dav/calendars/u/personal/`.
4. `here` is `-public-url` plus `/alborz/dav/`; without `-public-url`
   its host is the machine's hostname, lowercased. An unreadable
   hostname is fatal at start.
5. Only the transport reads markers. It routes by the marker's host,
   and between two sources on one host by the longest base path; it
   strips the marker, applies the source's credentials (ADR 31), and
   marks every href and `Location` in the answer. `here` is answered
   in-process, above the mirror. Above the transport a path is an
   opaque string.
6. A path on a host that is none of the account's sources fails.

### Instance identity

7. Every answer under `/alborz/dav/` carries `Alborz-Instance`, a
   random name kept in `alborz.meta`, shared by every process of one
   deployment.
8. A mail-host, domain or account-named server whose answer carries
   this instance's name is `here`, not another source. The domain's server is asked
   once, on first use, not at start.

### Use

9. The collections of every source form one list, grouped by source in
   the order of rule 1, each group named by its place (`place.<id>`)
   and, for a remote source, its host (ADR 35). Collections kept here,
   shared by another account or filled from a subscribed feed among
   them, are in `here`'s group. A source that does not answer is left
   out and a notice names its account; the others are listed.
10. Writes go to the object's source. New collections and imports go
    to the source the reader picks; the account's default
    (`Services.Default`, set on the servers page) is preselected, else
    the first source.
11. `/alborz/dav/` remains the address DAV clients use.
12. A collection's page MUST show its place as a read-only value
    (`collection.storagewhere`).

## Consequences

- Collections of an account-named server, previously bare paths, are
  now under that server's marker; old selections and links to them no
  longer resolve.
- One path on two servers, or one server under two names, is kept as
  separate copies.

## References

- RFC 4791, RFC 6352, RFC 6764
- ADR 22, ADR 24, ADR 31, ADR 35, ADR 53
- `pim/dav/provider.go` (`Source`, `Sources`, `SourceOf`, `route`,
  `marker`, `hereSource`, `isHere`), `pim/dav/collection.go`
  (`ListCollections`, `ByPlace`), `pim/pooled.go` (`Visible`),
  `pim/web/pages.go`, `templates/pages/pim/collection.html`,
  `templates/partials/rail.html` (`place`), `pim/dav/discover.go` (`OnMailHost`,
  `answersAs`), `pim/dav/transport.go`, `pim/dav/services.go`
  (`Services.Default`), `pim/web/owndav.go`, `server.go`
  (`InstanceHeader`), `data.go` (`dataMeta.Instance`)
