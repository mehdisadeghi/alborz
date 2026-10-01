# Installation

Installing on a server, assuming existing IMAP and SMTP servers.

    go build -o alborz ./cmd/alborz
    sudo install alborz /usr/local/bin/
    sudo mkdir -p /var/lib/alborz

The binary is self-contained.

There is no config file; the arguments are the configuration. Example
unit for `/etc/systemd/system/alborz.service`:

    [Unit]
    Description=alborz webmail
    Wants=network-online.target
    After=network-online.target

    [Service]
    User=alborz
    Group=alborz
    WorkingDirectory=/var/lib/alborz
    CacheDirectory=alborz
    Environment=XDG_CACHE_HOME=/var/cache
    EnvironmentFile=/etc/default/alborz
    ExecStart=/usr/local/bin/alborz example.org
    Restart=on-failure
    NoNewPrivileges=yes
    PrivateTmp=yes
    ProtectSystem=strict
    ProtectHome=yes

    [Install]
    WantedBy=multi-user.target

Generate the login key with `openssl rand -base64 32` and keep it in
`/etc/default/alborz` (mode 600) as `LBRZ_LOGIN_KEY=...`. It is read
from the environment, so it never appears in the unit or the process
list.

`CacheDirectory=alborz` gives the service `/var/cache/alborz`, and
`XDG_CACHE_HOME` points alborz's default cache there. Copies of remote calendars,
address books and calendar feeds are kept there between runs, sealed
under the login key, so a restart starts warm. `-cache=false` keeps no cache.

## Custom stylesheet

A `custom.css` in the data directory (`-data-dir`, here `/var/lib/alborz`)
is linked after the theme on every page, so it overrides any style
except the browser's status bar, which takes the theme's `--theme-color`.
alborz reads it at startup and on SIGUSR1; without it nothing is linked.

## DNS

A bare domain argument relies on SRV records for discovery. Per served
mail domain:

    _imaps._tcp        SRV 0 1 993  imap.example.org.
    _submissions._tcp  SRV 0 1 465  smtp.example.org.
    _sieve._tcp        SRV 0 1 4190 imap.example.org.
    _carddavs._tcp     SRV 0 1 443  dav.example.org.
    _caldavs._tcp      SRV 0 1 443  dav.example.org.

The records belong at the mail domain apex: `user@example.org` is
discovered via `_imaps._tcp.example.org`; a record under
`mail.example.org` serves `user@mail.example.org` instead. A domain
without working IMAP or SMTP discovery is dropped at startup with a
warning; missing sieve or DAV records just disable those features for
the domain. Explicit `domain=url` arguments bypass discovery.

## Multiple domains

One instance serves any number of mail domains, one argument per
domain; logins are only accepted for the listed domains. Each domain
resolves its own IMAP, SMTP, sieve, and DAV endpoints, via SRV or
explicitly:

    ExecStart=/usr/local/bin/alborz example.org \
        example.com=imaps://imap.migadu.com example.com=smtps://smtp.migadu.com

Domains may live on unrelated providers. Features follow the domain:
without a sieve server Filters is empty, and without DAV servers
calendars and contacts are kept in alborz. Accounts across domains can be signed in
side by side and switched from the account menu. Plain URL arguments
without a domain keep the upstream behavior instead: one provider,
any login accepted; the two forms cannot be mixed.

## Passwords

By default there is one credential: the login username and password
authenticate IMAP, SMTP, and sieve, and are also forwarded as HTTP Basic
auth to the CalDAV/CardDAV server. A DAV server with its own user
database (e.g. Nextcloud) may want another password, such as an app
password; a reader enters it once under Settings and alborz uses it
from then on.

That password is kept in the account's IMAP METADATA, or in alborz.db
where the server has none, encrypted under a key derived from the mail
password. A change made in alborz seals it under the new password; after
a change made elsewhere it no longer opens, and alborz asks for it again.

## Password changes

alborz does not touch the mail server's user database. It asks a
password server on the mail host over the poppassd protocol (USER,
PASS, NEWPASS), named per domain like the other upstreams:

    example.org=poppassd://127.0.0.1:106
    example.org=poppassd+unix:///run/poppassd.sock

With one configured, Settings > Password changes the password of the
domain's accounts; without, the page says it is not set up. A refused
current password counts against the reader like a refused sign-in. After
a change, alborz signs the account in again with the new password, keeps
it for this browser if it was remembered, and signs every other browser
out of the account. Mail, calendar and contacts apps are not signed out.

poppassd sends passwords in plain text, so alborz refuses to start with
a poppassd server that is not on `localhost`, a loopback address or a
Unix socket. A server on another host is reached through a tunnel
ending on loopback.

For system users (PAM), run
[poppassd-ceti](https://github.com/kravietz/poppassd-ceti) from its
systemd socket unit (port 106 on localhost) and add `?user=local`, since
PAM knows `alice`, not `alice@example.org`:

    example.org=poppassd://127.0.0.1:106?user=local

With user names wrong, poppassd-ceti answers "Old password is incorrect".

For Dovecot users in a passwd-file or an SQL database, or FreeBSD
system users, run alborz's own password server, alborzmail/passwd,
which speaks the same protocol on a Unix socket and refuses to listen
beyond loopback. It takes the full address as the user; FreeBSD system
users need `?user=local`.

## Reverse proxy

Behind nginx with TLS (certificates via certbot):

    server {
        server_name webmail.example.org;

        location / {
            proxy_pass http://127.0.0.1:1323;
            proxy_http_version 1.1;
            proxy_set_header Host $host;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            # alborz accepts attachments up to 32 MiB
            client_max_body_size 32m;
        }

        listen 443 ssl;
        ssl_certificate /etc/letsencrypt/live/webmail.example.org/fullchain.pem;
        ssl_certificate_key /etc/letsencrypt/live/webmail.example.org/privkey.pem;
    }

Run alborz with `-trusted-proxy 127.0.0.1` behind it: `X-Forwarded-Proto` and
`X-Forwarded-For` are believed only from a trusted proxy, and behind TLS
alborz marks its cookies Secure only when that proxy says the request came
over https.
