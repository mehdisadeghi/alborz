# ADR 12: Full-text index setting

Status: accepted.

## Context

A bare search term is sent as SEARCH TEXT when the server has a
full-text index, and as a header search (From, To, Cc, Subject)
otherwise. alborz detects an index by the `SEARCH=FUZZY` capability
(RFC 6203). Dovecot advertises it only for fts backends flagged
`FTS_BACKEND_FLAG_FUZZY_SEARCH` (fts-solr); fts-xapian and
fts-flatcurve index bodies and advertise nothing. No other capability
or ID field announces an index.

## Decision

1. `SEARCH=FUZZY` remains the primary signal.
2. Each account has a setting "The server indexes message bodies"
   (`IndexedSearch`), off by default, stored in the account's store.
3. Search MUST treat a stated index exactly like an advertised one:
   bare terms become SEARCH TEXT.
4. The Servers page shows one "Indexed search" row; when the setting,
   not the capability, supplies it, the row says "(your setting)".
5. alborz MUST NOT probe for an index (e.g. by timing a body search).

## Consequences

- An operator with xapian or flatcurve sets the option once per
  account; servers advertising the capability are unaffected.
- A wrong statement on a server without an index makes bare-term
  searches slow; they stay under the listing's bound (10 s) and fail
  as a slow server (ADR 33).

## References

- RFC 9051 section 6.4.4 (SEARCH), RFC 6203 (SEARCH=FUZZY)
- ADR 21, ADR 33
- `mail/views.go` (`SearchesIndex`), `mail/imapsearch/criteria.go`
  (`Criteria`), `mail/settings.go` (`IndexedSearch`),
  `settings/web/servers.go` (`abilities`), `mail/web/account.go`
