# Alborz

[![build](https://github.com/mehdisadeghi/alborz/actions/workflows/build.yml/badge.svg)](https://github.com/mehdisadeghi/alborz/actions/workflows/build.yml)
[![release](https://img.shields.io/github/v/release/mehdisadeghi/alborz)](https://github.com/mehdisadeghi/alborz/releases/latest)
[![image](https://img.shields.io/badge/image-ghcr.io-blue?logo=docker&logoColor=white)](https://github.com/mehdisadeghi/alborz/pkgs/container/alborz)

![](shahalborz.jpg)
*Shahalborz, Alborz mountains. Image based on a [photo by nomad] on
SummitPost.*

Multi-account, RTL-ready mail user agent with calendars, contacts,
tasks and sieve filters in a single binary.

It works with your existing IMAP, SMTP, ManageSieve, CalDAV and
CardDAV servers. Settings are stored on those servers where the
protocol allows it; a local database holds remembered sign-ins and
anything a server cannot store.

## Features

- Multiple accounts with unified views
- Search across folders with Gmail-style operators
- CalDAV calendars: month, day and agenda views
- CalDAV tasks (VTODO)
- CardDAV contacts with photos, groups, categories
- iCalendar feed subscriptions
- Built-in CalDAV/CardDAV server with sharing
- Multiple DAV servers per account
- Sieve filters: rules, forwarding, vacation
- Per-account identities and signatures
- Coloured stars on mail, contacts, events, tasks
- Gmail-style keyboard shortcuts
- Responsive PWA with light and dark themes
- Offline reading of visited pages
- Passkey (WebAuthn) lock
- Session list with remote sign-out
- RTL; English, German, Persian and Spanish
- Solar Hijri calendar alongside Gregorian
- mbox and .eml import and export
- Drag-and-drop mail import
- Calendar and address book archive import/export
- Multiple mail domains per instance
- No-JavaScript baseline

## Standards

In addition to the protocols above:

| RFC | Scope |
|---|---|
| 5546, 6047 | iTIP and iMIP meeting requests: display, reply, file and send |
| 3156, Autocrypt, WKD | PGP/MIME signature verification; key discovery |
| 8601 | Authentication-Results from a trusted server only |
| 8058, 2369, 2919 | One-click unsubscribe, list headers |
| 2177, 2971, 5464, 5256, 6154, 4551 | IDLE, ID, METADATA, SORT and THREAD, special-use folders, CONDSTORE |
| 4315, 5819, 6851 | UIDPLUS, LIST-STATUS, MOVE |
| 5228, 5804, 5230, 6609, 5260 | Sieve and ManageSieve: rules, forwarding and vacation replies |
| 5545, 7986, 7529 | iCalendar: VTIMEZONE generation, recurrence, alarms, COLOR, RSCALE |
| 4791, 6352, 6764 | CalDAV and CardDAV server, `.well-known` discovery |
| 4918, 7232 | WebDAV dead properties, conditional requests |
| 3676, 5322 | format=flowed, References, Reply-To, signature delimiter |
| 6350, 6186 | vCard, SRV service discovery |
| WebAuthn | Passkeys |

## Install

Release binaries for linux/amd64, arm64 and armv7 are attached to every
[release]; see [INSTALLATION.md](INSTALLATION.md) for a systemd unit and
for building from source.

A container image is published at `ghcr.io/mehdisadeghi/alborz`, tagged
`latest` and per release:

    docker run -p 1323:1323 -v alborz-data:/data -e LBRZ_LOGIN_KEY=<key> ghcr.io/mehdisadeghi/alborz example.org

Without the volume and the key it still runs, but forgets sign-ins on a
restart and keeps its data inside the container.

## Usage

With SRV DNS records set up (see [RFC 6186]):

    alborz example.org

With upstream servers named explicitly:

    alborz imaps://mail.example.org:993 smtps://mail.example.org:465

Several mail domains, one argument per domain:

    alborz example.org example.com=imaps://mail.example.com example.com=smtps://mail.example.com

See `docs/cli.md` for every flag.

## Contributing

Report issues and send patches at [github.com/mehdisadeghi/alborz].

## Acknowledgements

Alborz began as a fork of [alps] and builds on the work of Simon Ser,
Drew DeVault and the other alps contributors.

## License

MIT

[alps]: https://sr.ht/~migadu/alps
[release]: https://github.com/mehdisadeghi/alborz/releases/latest
[photo by nomad]: https://www.summitpost.org/shah-alborz-north-west/389048/c-154044
[RFC 6186]: https://tools.ietf.org/html/rfc6186
[github.com/mehdisadeghi/alborz]: https://github.com/mehdisadeghi/alborz
