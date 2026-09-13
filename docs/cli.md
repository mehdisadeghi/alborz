# SYNOPSIS

    alborz [options...] <upstream servers...>

# DESCRIPTION

alborz is a simple and extensible webmail. It offers a web interface for IMAP,
SMTP and other upstream servers.

At least one upstream IMAP server needs to be specified. The easiest way to do
so is to just specify a domain name:

    alborz example.org

This assumes SRV DNS records are properly set up (see [RFC 6186]).

Alternatively, one or more upstream server URLs can be specified:

    alborz imaps://mail.example.org:993 smtps://mail.example.org:465

The following URL schemes are supported:

* `imaps` (IMAP with TLS), `imap` (IMAP with STARTTLS), `imap+insecure`
  (plain IMAP)
* `smtps` (SMTP with TLS), `smtp` (SMTP with STARTTLS), `smtp+insecure`
  (plain SMTP)
* `sieve` (ManageSieve with STARTTLS, port 4190 by default),
  `sieve+insecure` (plain ManageSieve)
* `https` (CardDAV and CalDAV over HTTPS), `http+insecure` (CardDAV and CalDAV
  over plain HTTP)
* `carddavs` (CardDAV over HTTPS), `carddav+insecure` (CardDAV over plain HTTP)
* `caldavs` (CalDAV over HTTPS), `caldav+insecure` (CalDAV over plain HTTP)
* `poppassd` (a poppassd password server over TCP, port 106 by default),
  `poppassd+unix` (the same on a Unix socket, `poppassd+unix:///run/poppassd.sock`);
  `?user=local` sends the address's local part as the user name. With one,
  Settings offers a password change for the domain's accounts. poppassd is
  plain text, so a `poppassd` host other than `localhost` or a loopback
  address is refused at startup.

# OPTIONS

**-addr**: listening address (default: ":1323")

**-pprof**: loopback address, such as `localhost:6060`, serving Go's profiles
under `/debug/pprof/` (default: unset, none served). Any other address is
refused: a heap profile holds passwords and mail.

**-debug**: enable debug logs, print IMAP network activity, and read
templates and assets from `./templates`

**-private-services**: let an account name a calendar or contacts server on
plain HTTP or a private address

**-trusted-proxy**: comma-separated addresses or CIDR ranges of the proxies in
front of alborz, whose `X-Forwarded-For` names the reader and
`X-Forwarded-Proto` the scheme; required behind a TLS-terminating proxy for
Secure cookies (default: unset, the connection's own address and scheme)

**-public-url**: where readers reach alborz, such as
`https://mail.example.org`, used for links in mail and addresses shown to other
clients; scheme and host only. Can also be given as `LBRZ_PUBLIC_URL`
(default: unset, each request's own)

**-login-key**: required key sealing what alborz keeps: remembered
logins, settings, the calendars and address books kept here, and the cache. Can also be given as the `LBRZ_LOGIN_KEY`
environment variable, which keeps it out of the process list.

**-cache**: keep what the servers answered in the user cache directory
(`$XDG_CACHE_HOME/alborz` or the platform's equivalent), so pages open without
asking again (default: true); `-cache=false` asks every time and keeps nothing

**-data-dir**: directory holding `alborz.db` and an optional `custom.css`; it
must exist (default: the working directory)

**-project-url**: where the footer's project name links (default: unset, the
name is printed as text)

**-motd**: directory of message-of-the-day collections, one `.toml` file per
language in the format of `addons/motd/example.toml`, offered as a status bar
segment; a directory alborz cannot read stops it starting (default: unset, no
segment)

**-h**, **--help**: show help message and exit

# SIGNALS

**SIGUSR1**: reloads templates and `custom.css`

**SIGINT**, **SIGTERM**: stop, giving requests in flight up to five seconds

# LOGIN-KEY

32 random bytes in base64; generate one with `openssl rand -base64 32`.
What alborz seals under a key opens only with that key. Each file it keeps
records the key it was sealed under, and alborz refuses to start on a file
another key sealed; restore the key, or move the file aside. A file written
before alborz sealed with AES-256-GCM, in the Fernet format, is refused the
same way.

[RFC 6186]: https://tools.ietf.org/html/rfc6186
