# ADR 108: Account names

Status: accepted.

## Context

A reader types a mail address with any capitals, and mail servers
deliver `Alice@example.org` and `alice@example.org` to one mailbox.
Sessions and logins were named by the address as typed, so one mailbox
under two spellings was two accounts, and a password change signed out
only one of them.

## Decision

1. An account's name is its mail address in lower case
   (`AccountName`). A configured domain is kept in lower case.
2. An address that arrives as text MUST be read as its account's name
   before it names a login: at a sign-in from the form, a remembered
   session or a DAV client's mail password (`loginManager.put`), and
   in an `account` parameter of a URL or a form (`Context.LoginFor`).
3. A sign-in with an address that is not its account's name MUST move
   the settings record and the account key kept under that address
   (ADR 51) to the account's name, where the account has none.
4. A remembered session that names such an address MUST name the
   account once it is restored.

## Consequences

- Two mailboxes a server tells apart by capitals are one account.
- Addresses a provider delivers to one mailbox by its own rules (plus
  addressing, aliases) are separate accounts.
- A remembered session of rule 4 is not listed among its account's
  sessions before its browser returns.
- A version without rule 3 looks for a moved record under the address
  as typed and finds none.

## References

- RFC 5321 2.4
- ADR 24, ADR 30, ADR 51
- `accounts.go` (`AccountName`), `login.go` (`loginManager.put`),
  `records.go` (`settingsRecords.adopt`), `web/accounts.go`
  (`LoginFor`), `web/session.go` (`restored`)
