# Alborz

[![build](https://github.com/mehdisadeghi/alborz/actions/workflows/build.yml/badge.svg)](https://github.com/mehdisadeghi/alborz/actions/workflows/build.yml)
[![release](https://img.shields.io/github/v/release/mehdisadeghi/alborz)](https://github.com/mehdisadeghi/alborz/releases/latest)

![](shahalborz.jpg)
*Shahalborz, Alborz mountains. Image based on a [photo by nomad] on
SummitPost.*

Multi-account, RTL-ready mail user agent with calendars, contacts,
tasks and sieve filters in a single binary.

It speaks IMAP, SMTP, ManageSieve, CalDAV and CardDAV to the servers
you already have. Settings live on the servers where they can; one
file of its own holds remembered logins and what a server cannot keep.

## What it does

- serves several mail domains, with logins limited to those domains
- an account switcher and a merged view across accounts, on every
  section
- full-text search, server-side sorting and threads; a star in seven
  colours on messages, contacts, events and tasks, with starred and
  colour views on every list
- a sieve filter editor, and rules, forwarding and auto-replies
  composed as scripts
- calendars with month, day and agenda views, tasks, and contacts
  with photos, groups and categories; collections created, edited and
  deleted in place; feeds subscribed by address
- named signatures and identities per account, chosen per message
- mail imported and exported as mbox, messages as .eml
- English, German, Persian and Spanish, with the Solar Hijri calendar
  beside the Gregorian one
- responsive, light and dark schemes, theme variants, installable on a
  phone; every page works without a script and takes htmx as an
  enhancement
- new mail pushed by IMAP IDLE, DAV traffic cached and revalidated by
  ctag

## Standards implemented

Beyond the five protocols above:

| RFC | What for |
|---|---|
| 5546, 6047 | iTIP and iMIP: meeting requests shown, answered, filed and sent |
| 3156, Autocrypt, WKD | PGP/MIME signature verification; keys from the sender's domain, headers and attachments |
| 8601 | Authentication-Results, read from a named trusted server only |
| 8058, 2369, 2919 | One-Click unsubscribe, list headers |
| 2177, 2971, 5464, 5256, 6154, 4551 | IDLE, ID, METADATA for settings, SORT and THREAD, special-use folders, CONDSTORE |
| 5228, 5804, 5230, 6609, 5260 | Sieve scripts over ManageSieve; rules, forwarding and auto-replies composed as scripts |
| 5545, 7986, 7529 | iCalendar with generated VTIMEZONE, recurrence expansion, alarms; COLOR on events and tasks; RSCALE for rules counted in another calendar; calendar colours through Apple's property |
| 3676, 5322 | format=flowed on send, References, Reply-To, signature delimiters |
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
