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
5. The fields of rules 6–8 MUST appear only on the review step, the one
   offering to finish setup, and MUST work without JavaScript.
6. Setup MUST offer a public address (`scheme://host[:port]`, no path),
   prefilled from the `Origin` of the request rendering the review step, else
   its `Host` and forwarded scheme. On the finishing POST the `Origin` header
   MUST replace a value left at its prefill. It MUST be `https`, or `http` for
   `localhost`, loopback or private addresses, and is saved as `public-url`
   with the domains. An empty value saves none.
7. A setup request from a loopback peer carrying `X-Forwarded-For` or
   `Forwarded` MUST offer "Trust the proxy on this machine", ticked by default;
   when ticked, `trusted-proxy = "127.0.0.1, ::1"` is saved with the domains.
8. A `-public-url` or `-trusted-proxy` already in force (environment, CLI or
   file) MUST be shown read-only and MUST NOT be written.
9. Saved values take effect at the next start. When finishing saved either,
   the sign-in page's notice MUST say to restart.

## Consequences

- The configuration and database must be backed up together; the key is needed
  to recover encrypted data.
- Environment-only deployments need no configuration writes.
- Optional DAV/Sieve endpoints can be entered during setup.
- Until the restart after setup, links use each request's own origin and a
  local proxy's forwarded headers are ignored.

## References

- RFC 6186, RFC 9051, RFC 5321, RFC 6454 (`Origin`), RFC 7239 (`Forwarded`); XDG Base Directory Specification
- ADR 104 (peers use `-public-url`)
- `internal/config/config.example.toml`, `internal/config/config.go`, `upstream_config.go`
- `setup.go`, `auth/web/setup.go`, `templates/pages/setup.html`, `web/server.go`
