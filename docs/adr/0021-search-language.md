# ADR 21: Search query language and scope

Status: accepted, amended by ADR 34 (one connection turn per folder,
envelope verification and `-in:` exclusion) and ADR 69 (shared `search`
grammar and cross-section `/search`).

## Context

Mail search needs a concise query syntax that maps directly to IMAP
`SEARCH` (RFC 9051 6.4.4) within a single folder or across all folders
of the accounts in scope.

## Decision

### Language

1. Queries parse into `search.Query` (ADR 69): bare words, quoted
   phrases, `key:value` (with optional quotes), and a leading `-` for
   negation.
2. In mail (`mail/imapsearch`), keys map to IMAP `SEARCH` criteria:
   - `from`, `to`, `cc`, `bcc`, `subject`, `list` (`List-Id`): `HEADER`;
   - `body`: `BODY`; `text`: `TEXT`;
   - `is:` `unread`, `read`, `starred`/`flagged`, `unstarred`,
     `answered`/`replied`, `draft`: IMAP flag criteria; `is:<colour>`
     (`alborz.FlagColors`): the colour's keyword criteria (two colours
     return 400, ADR 69);
   - `after`, `before` (`YYYY-MM-DD` or `YYYY/MM/DD`): `SINCE`,
     `BEFORE`;
   - `newer_than`, `older_than` (`d`, `w`, `m`, `y`): `SINCE`, `BEFORE`;
   - `larger`, `smaller` (`K`, `M`): `LARGER`, `SMALLER`;
   - `in`: folder scope, never an IMAP criterion.
3. An unknown key, or a known key with an unparseable value, MUST be
   searched as text rather than dropped.
4. Bare terms match `From`, `To`, `Cc` and `Subject`, or `TEXT` when the
   server's full-text index is advertised or stated (ADR 12).
5. `has:attachment`, `OR` and parenthesised grouping are not supported.

### Scope

6. A folder's search box searches that folder; if the query contains
   `in:`, it redirects to `/mail/search`.
7. `/mail/search` (`mail.search`) searches every folder of the accounts
   in scope, one `DoIMAPWork` turn per folder (ADR 34), running accounts
   in parallel and merging results.
8. `junk` and `trash` role folders are excluded unless `in:anywhere` or
   `in:all` is given. `in:<folder>` scopes to one role or folder name;
   `-in:<folder>` excludes one.
9. Each result row carries its account, folder and UID, and links its
   folder chip to that folder's search with `in:` stripped.

## Consequences

- Cross-folder mail search costs one `SELECT`, `SEARCH` and `FETCH` turn
  per folder per account.

## References

- RFC 9051 6.4.4 (`SEARCH`)
- ADR 12, ADR 34, ADR 69
- `search/query.go`, `mail/imapsearch/`, `mail/listing.go`
  (`searchFolders`, `searchWindow`, `Everywhere`),
  `mail/web/searchall.go`, `mail/web/mailbox.go` (`searchInstead`)
