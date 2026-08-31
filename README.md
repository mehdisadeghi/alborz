# Alborz

![](shahalborz.jpg)
*Shahalborz, Alborz mountains. Image based on a [photo by nomad] on
SummitPost.*

A simple and pragmatic webmail. Alborz is a fork of [alps].

## Fork additions

- serves multiple mail domains, logins whitelisted by domain
- account switcher and a unified mail view across accounts
- full-text body search, server-side sorting, and a starred view
- sieve filter editor
- CalDAV tasks, calendar month, date and list views, and collection CRUD
- extended vCard contact fields with photos
- named signatures per account, chosen per message
- mail exported as .eml and mbox
- UI in English, German, Persian, and Spanish, with the Solar Hijri
  calendar as a second reckoning beside the Gregorian one
- responsive UI with dark scheme and theme variants
- JavaScript optional: every page works without one
- push, not polling: an IMAP IDLE watcher per account
- CalDAV/CardDAV caching at the HTTP transport with ctag revalidation

### Standards implemented

Beyond IMAP, SMTP, ManageSieve, CalDAV and CardDAV:

- **RFC 5546 / 6047** iTIP and iMIP - meeting requests are shown,
  answered, filed and sent, with cancellations and replies
- **RFC 3156** PGP/MIME signature verification, with **Autocrypt** and
  attached keys as key sources
- **RFC 8601** Authentication-Results, read only from a named trusted
  server and shown only on failure
- **RFC 8058** One-Click unsubscribe, with mailto and link fallbacks
- **RFC 2177** IDLE, **RFC 2971** ID, **RFC 5464** METADATA for settings
- **RFC 5545** iCalendar, including VTIMEZONE generated for what it
  writes, recurrence expansion and alarms
- **RFC 3676** format=flowed, **RFC 5322** References, Reply-To and
  signature delimiters
- **RFC 2369** list headers, **RFC 6350** vCard, **RFC 6186** SRV
  discovery

See [INSTALLATION.md](INSTALLATION.md) for building and a systemd unit.

## Usage

Assuming SRV DNS records are properly set up (see [RFC 6186]):

    go run ./cmd/alborz example.org

To manually specify upstream servers:

    go run ./cmd/alborz imaps://mail.example.org:993 smtps://mail.example.org:465

To serve multiple mail domains, one argument per domain:

    go run ./cmd/alborz example.org example.com=imaps://mail.example.com example.com=smtps://mail.example.com

See `docs/cli.md` for more information.

When developing themes and plugins, the script `contrib/hotreload.sh` can be
used to automatically reload alborz on file changes.

## Contributing

Report issues and send patches at [github.com/mehdisadeghi/alborz].

## Acknowledgements

This fork builds on the work of Simon Ser, Drew DeVault, and the other
[alps] contributors.

## License

MIT

[alps]: https://sr.ht/~migadu/alps
[photo by nomad]: https://www.summitpost.org/shah-alborz-north-west/389048/c-154044
[RFC 6186]: https://tools.ietf.org/html/rfc6186
[github.com/mehdisadeghi/alborz]: https://github.com/mehdisadeghi/alborz
