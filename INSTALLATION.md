# Installation

Installing on a server, assuming existing IMAP and SMTP servers.

    go build -o alborz ./cmd/alborz
    sudo install alborz /usr/local/bin/
    sudo mkdir -p /var/lib/alborz
    sudo cp -r themes plugins /var/lib/alborz/

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
    EnvironmentFile=/etc/default/alborz
    ExecStart=/usr/local/bin/alborz example.org
    Restart=on-failure
    NoNewPrivileges=yes
    PrivateTmp=yes
    ProtectSystem=strict
    ProtectHome=yes

    [Install]
    WantedBy=multi-user.target

Generate the login key with
`go run github.com/fernet/fernet-go/cmd/fernet-keygen` and keep it in
`/etc/default/alborz` (mode 600) as `LBRZ_LOGIN_KEY=...`. It is read
from the environment, so it never appears in the unit or the process
list.

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
users of a domain without sieve or DAV servers simply see no Filters,
Contacts, or Calendar. Accounts across domains can be signed in
side by side and switched from the account menu. Plain URL arguments
without a domain keep the upstream behavior instead: one provider,
any login accepted; the two forms cannot be mixed.

## Passwords

There is one credential: the login username and password authenticate
IMAP, SMTP, and sieve, and are also forwarded as HTTP Basic auth to the
CalDAV/CardDAV server. A DAV server with its own user database (e.g.
Nextcloud) must accept the same username and password, and it is on you
to keep them aligned; when they drift, mail keeps working while
contacts and calendars fail.

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

`X-Forwarded-Proto` matters: alborz marks its cookies Secure based on it.
