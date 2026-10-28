# ADR 98: First-run mail setup

Status: accepted.

## Context

A new installation needs guided server configuration and key creation.
Deployment configuration must remain editable independently of application data.

## Decision

1. Configuration MUST use TOML in the OS user configuration directory unless
   selected with `-config` or `LBRZ_CONFIG`. Environment values MUST override
   explicit CLI values, then file values, then defaults. Domain tables MUST
   distinguish IMAP, SMTP, CalDAV, CardDAV, Sieve and password-change services.
2. A fresh installation MUST persist its generated login key in an owner-readable
   configuration file before creating encrypted data. Explicit keys MUST take
   precedence. Existing encrypted data with a missing key MUST cause an error.
3. A console owner code MUST authorize discovery and setup completion. Setup
   MUST verify mail connections over TLS, keep pending domains in memory until
   completion, save their server settings together, then redirect
   to login. It MUST NOT request a mail password or create an authenticated login.
   After completion, domain changes MUST use configuration, environment or CLI.
4. Configuration writes MUST preserve comments and refuse concurrent edits.

## Consequences

- The configuration and database must be backed up together; the key is needed
  to recover encrypted data.
- Environment-only deployments need no configuration writes.
- Optional DAV/Sieve endpoints can be entered during setup.

## References

- RFC 6186, RFC 9051, RFC 5321; XDG Base Directory Specification
- `cmd/alborz/config.example.toml`, `cmd/alborz/config.go`, `upstream_config.go`
- `setup.go`, `auth/web/setup.go`, `templates/pages/setup.html`
