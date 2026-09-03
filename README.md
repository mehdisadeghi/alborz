# Alborz

[![build](https://github.com/mehdisadeghi/alborz/actions/workflows/build.yml/badge.svg)](https://github.com/mehdisadeghi/alborz/actions/workflows/build.yml)
[![release](https://img.shields.io/github/v/release/mehdisadeghi/alborz)](https://github.com/mehdisadeghi/alborz/releases/latest)

![](shahalborz.jpg)
*Shahalborz, Alborz mountains. Image based on a [photo by nomad] on
SummitPost.*

Multi-account, RTL-ready mail user agent with calendars, contacts,
tasks and sieve filters in a single binary.

It speaks IMAP, SMTP, ManageSieve, CalDAV and CardDAV to the servers
you already have, with no database and no script required.

## What it does

- serves several mail domains, with logins limited to those domains
- an account switcher and a merged view across accounts
- full-text search, server-side sorting, threads and a starred view
- a sieve filter editor
- calendars and tasks with month, date and list views; contacts with
  photos; collections created, edited and deleted in place
- named signatures and identities per account, chosen per message
- mail exported as .eml and mbox
- English, German, Persian and Spanish, with the Solar Hijri calendar
  beside the Gregorian one
- responsive, dark scheme, theme variants, every page usable without a
  script
- new mail pushed by IMAP IDLE, DAV traffic cached and revalidated by
  ctag

## Standards implemented

Beyond the five protocols above:

| RFC | What for |
|---|---|
| 5546, 6047 | iTIP and iMIP: meeting requests shown, answered, filed and sent |
| 3156, Autocrypt | PGP/MIME signature verification, keys from headers and attachments |
| 8601 | Authentication-Results, read from a named trusted server only |
| 8058, 2369 | One-Click unsubscribe, list headers |
| 2177, 2971, 5464 | IDLE, ID, METADATA for settings |
| 5545 | iCalendar with generated VTIMEZONE, recurrence expansion, alarms |
| 3676, 5322 | format=flowed, References, Reply-To, signature delimiters |
| 6350, 6186 | vCard, SRV discovery |

## Install

Release binaries for linux/amd64, arm64 and armv7 are attached to every
[release]; see [INSTALLATION.md](INSTALLATION.md) for a systemd unit and
for building from source.

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
