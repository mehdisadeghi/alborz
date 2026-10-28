# ADR 22: Account-named CalDAV and CardDAV servers

Status: accepted, amended by ADR 28 (the named server is a source
beside the domain's, not a replacement) and ADR 31 (checked when
dialled, and which login each source takes).

## Context

DAV servers come from the operator's upstreams or the domain's SRV
records (RFC 6764). An account whose domain publishes neither, or whose
calendars live elsewhere, needs to name its own.

## Decision

1. An account MAY name a CalDAV URL, a CardDAV URL and a username,
   each optional (`dav.Services`). An empty username is the mail
   address.
2. The password is the account's HTTP password (ADR 5), else the mail
   password.
3. `Services` is kept in the account's store: METADATA (RFC 5464) on
   its mail server, else `alborz.db`. It follows the account to every
   browser and instance.
4. Unless the operator passes `-private-services`, a named server MUST
   be `https` and MUST NOT resolve to a loopback, private, link-local or
   RFC 6598 address. The check runs when the form is saved
   (`CheckServiceURL`) and again at dial time (ADR 31).
5. Saving the servers forgets the account's cached state
   (`Server.ForgetAccount`): discovered collections are dropped and
   mirrored copies stop syncing; the copies stay, keyed by the old
   server's host (ADR 53).
6. Sieve has no override: ManageSieve is the mail domain's, dialled by
   domain.

## Consequences

- The named server is the `own` source (ADR 28).

## References

- RFC 6764, RFC 5464, RFC 6598
- ADR 5, ADR 28, ADR 31, ADR 53
- `pim/dav/services.go` (`Services`, `LoadServices`), `services.go`
  (`CheckServiceURL`), `remote.go` (`publicAddr`, `refuseLocal`),
  `pim/web/services.go`, `cmd/alborz/main.go` (`-private-services`)
