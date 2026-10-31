# ADR 104: Alborz instances announced through METADATA

Status: accepted. Amends ADR 28 (the `peer` source) and ADR 31 (how a
peer is dialled).

## Context

An account can keep collections on several alborz instances, for
example one at home and one at work. Each instance lists only its own
collections and the servers it is configured with or the account
names, so the others stay hidden unless the account names one by hand,
and then only one. The account's settings already travel with its mail
server through IMAP METADATA (ADR 52), which every instance reads.

## Decision

### Announcing

1. An instance with `-public-url` set MUST list its `here` address
   (`-public-url` plus `/alborz/dav/`) in the account setting
   `dav.peers` (`PeersKey`, a JSON list of URLs) while the account
   owns at least one collection of any kind there, and MUST remove it
   once the account owns none. Without `-public-url` it MUST NOT
   announce: the hostname fallback is not an address another instance
   can reach.
2. The list holds at most 8 addresses (`maxPeers`), newest first; an
   address is listed once.
3. The setting MUST be written only when this instance's presence in
   it is wrong, checked when the account's collection list is loaded
   (`Collections`), at most once per account and service per process, and again
   after a collection is made or deleted. It MUST NOT be written where
   the account's settings are not on its mail server.

### Reading

4. `Sources` MUST add one `peer` source per listed address, after
   `host` and before `here`, skipping an address that is not HTTPS
   (plain HTTP only with `-private-services`), whose host is this
   instance's or any earlier source's, or that answers with this
   instance's `Alborz-Instance` (ADR 28 rule 8).
5. A peer MUST be dialled and probed through `Outgoing.Guarded` as an
   account-named server is (ADR 22 rule 4, ADR 31 rule 3). It is not
   `Named` and takes the mail login (ADR 31 rule 1).
6. A peer that does not answer is left out with a notice, as any
   source (ADR 28 rule 9).

## Consequences

- Instances sharing an account find each other with no setting, once
  each has a collection of the account's and a public address.
- The mail login is sent to every listed instance. Only the account
  can write its METADATA, so only an instance it signed in to can list
  one.
- On a mail server without METADATA no instance is announced.
- Collections on two peers share the place id `peer`; a new
  collection placed on `peer` goes to the first listed.
- An instance that loses its public address or its data does not take
  its old address out; others skip it as an unanswering source until
  the account's last collection there is deleted from a live instance.

## References

- RFC 5464, RFC 4791, RFC 6352
- ADR 22, ADR 28, ADR 31, ADR 52
- `pim/dav/provider.go` (`SourcePeer`, `PeersKey`, `maxPeers`,
  `peers`, `announce`, `isHere`), `pim/dav/collection.go` (`ByPlace`),
  `pim/dav/transport.go` (`send`), `pim/store/store.go` (`Keeps`),
  `pim/web/services.go` (`placeIDs`)
- Tests: `pim/dav/discovery_test.go` (`TestPeersAnnouncedAndRead`)
