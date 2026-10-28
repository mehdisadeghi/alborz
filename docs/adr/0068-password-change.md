# ADR 68: Mail password change over poppassd

Status: accepted, amended by ADR 72 (the account's key is rewrapped
under the new password). Amends ADR 5.

## Context

alborz runs unprivileged, often on another host than the mail server,
and the user database differs per server (system users, Dovecot
passwd-file, SQL, LDAP). Three things depend on the mail password: the
open logins, the password each remembered browser keeps sealed under
its cookie secret, and the DAV password sealed under a key derived
from the mail password (ADR 5).

## Decision

1. alborz MUST NOT write the mail server's user database. It changes a
   password over poppassd only (USER, PASS, NEWPASS, QUIT).
2. A domain names its password server as an upstream:
   `poppassd://host[:106]` or `poppassd+unix:///path`. The only option
   is `?user=local`, which sends the address's local part. A missing
   host or socket path stops startup.
3. poppassd is plain text: a `poppassd://` host other than `localhost`
   or a loopback address MUST stop startup. A remote server is reached
   through a tunnel ending on loopback.
4. The dial is bounded by 3 s and the conversation by the sign-in
   timeout (20 s).
5. Settings > Password (`/settings/password`) is in every account's
   rail. Without a password server the form renders with an alert that
   changes are not set up for the domain, and a submission is refused
   the same way.
6. A poppassd refusal is shown as a form alert with the server's text.
   A refusal before NEWPASS counts as a refused sign-in (ADR 30). A
   server that does not answer is a form alert.
7. After NEWPASS succeeds:
   - the DAV password's random key is wrapped again under the new
     password with a new salt; one the old password did not open stays
     as it is (ADR 5);
   - the account signs in with the new password and replaces its login
     in this browser;
   - where this browser remembered the account, it keeps the new
     password;
   - every other browser session of the account, live or remembered,
     is signed out.
8. If the new sign-in fails, the alert says the password changed and
   the sign-in failed; the current login continues until it needs the
   password again.

## Consequences

- Mail, calendar and contacts apps are not signed out; the page says
  so.
- alborz's DAV server caches an accepted password for 5 minutes, so the
  old password may be accepted that long after a change.
- A loopback port is open to every local user; a Unix socket with its
  group limited to alborz is tighter.
- No reset of a forgotten password.
- alborz-passwd (poppassd for Dovecot passwd-file, Dovecot SQL and
  FreeBSD system users) also refuses to listen beyond loopback.

## References

- ADR 5, ADR 30
- `passwd.go` (`parsePasswdUpstream`, `ChangePassword`,
  `passwdServer.change`), `secret.go` (`rewrap`),
  `settings/web/password.go`, `web/sessions.go` (`PasswordChanged`),
  `pim/web/owndav.go` (`davPasswordLife`)
