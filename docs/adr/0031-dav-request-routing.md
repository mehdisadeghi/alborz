# ADR 31: Per-source DAV request routing

Status: accepted, amended by ADR 104 (the `peer` source is dialled as
`own`). Amends ADR 22 (servers are checked at dial time, not only when
saved) and ADR 28 (which credentials each source takes).

## Context

ADR 28 routes a request by the `/@host` marker on its path. The
credentials, redirect handling, dialer and per-host limit must also
follow the source, at every hop, including hrefs inside request bodies
(`calendar-multiget`, `addressbook-multiget`).

## Decision

`sourceRouter` resolves the source of every request by `route` and
`send` applies the following per hop.

1. **Credentials.** Exactly one source per account is `Named` and
   takes the username and password the account keeps for its servers
   (ADR 5, ADR 22): the account's own server; the domain or mail-host
   source when the named server turns out to be that source; the
   domain source when the account names none. Every other source takes
   the mail login.
2. Credentials MUST be sent only to the source's own host. A hop to
   another host is sent without `Authorization`.
3. **Dialling.** The `own` and `peer` sources, and the probe asking
   whether either is this alborz, MUST be dialled through
   `Outgoing.Guarded`, which refuses non-public addresses at connect
   time (`refuseLocal`, `publicAddr`), covering redirects and DNS
   rebinding. `-private-services` lifts this, as it lifts the check
   when saved. Deployment-configured sources (`domain`, `host`) use
   `Outgoing.Trusted`.
4. **Redirects** keep method and body, up to `maxRedirects` (10). A
   redirect from an HTTPS source to a non-HTTPS URL MUST be refused.
   A 401 at any hop is a `RefusedError` naming that hop's host.
5. **Bodies.** Hrefs in a request body lose the marker, as hrefs in a
   response gain it.
6. **Limits.** The per-host connection limit (4, of which 2 for
   background work) sits inside both `Outgoing` transports, keyed by
   the scheme and host actually dialled. Background work is marked with
   `alborz.Background`.

## Consequences

- A server that answers with hrefs on another host (e.g. iCloud
  partitions) is asked there without credentials and refuses. Naming
  that host as the account's server works around it.
- `publicAddr` is the single rule for "public address" and excludes
  RFC 6598 shared address space (100.64.0.0/10).

## References

- RFC 4918, RFC 4791 7.9, RFC 6352 8.7, RFC 9110 15.4 (3xx), RFC 6598
- ADR 5, ADR 22, ADR 28, ADR 104
- `pim/dav/transport.go` (`sourceRouter`, `send`, `unmarked`,
  `marked`, `maxRedirects`, `RefusedError`), `pim/dav/provider.go`
  (`Sources`, `Source.Named`, `route`), `remote.go` (`Outgoing`,
  `limitedTransport`, `refuseLocal`, `publicAddr`, `Background`)
- Tests: `pim/dav/collection_test.go`
  (`TestASourceIsAskedInItsOwnPathsAndAnswersInOurs`,
  `TestALoginStaysOnItsSourcesHost`,
  `TestAnAccountsServerIsNotDialledOnOurNetwork`)
