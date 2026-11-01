# Alborz

[![build](https://github.com/mehdisadeghi/alborz/actions/workflows/build.yml/badge.svg)](https://github.com/mehdisadeghi/alborz/actions/workflows/build.yml)
[![release](https://img.shields.io/github/v/release/mehdisadeghi/alborz)](https://github.com/mehdisadeghi/alborz/releases/latest)
[![image](https://img.shields.io/badge/image-ghcr.io-blue?logo=docker&logoColor=white)](https://github.com/mehdisadeghi/alborz/pkgs/container/alborz)

![](shahalborz.jpg)
*Shahalborz, Alborz mountains. Image based on a [photo by nomad] on
SummitPost.*

Multi-account, RTL-ready mail user agent with calendars, contacts,
tasks and sieve filters in a single binary.

**Status: alpha.** Expect breaking changes before 1.0, and back up `alborz.db`.

Alborz works with your existing IMAP, SMTP, ManageSieve, CalDAV and
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
- Optional passkey (WebAuthn) lock; requires JavaScript
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
| 5546, 6047 | iTIP and iMIP event requests, replies and cancellations |
| 3156, Autocrypt, WKD | PGP/MIME signature verification; Autocrypt and WKD key lookup |
| 8601 | Authentication-Results from the configured `authserv-id` |
| 2369, 2919 | List headers and unsubscribe links |
| 2177, 2971, 5464, 5256, 6154, 4551 | IDLE, ID, METADATA, SORT/THREAD, special-use folders, CONDSTORE |
| 4315, 5819, 6851 | UIDPLUS, LIST-STATUS, MOVE |
| 5228, 5804, 5230, 6609, 5260 | Sieve and ManageSieve: rules, forwarding and vacation replies |
| 5545, 7986, 7529 | iCalendar recurrence, alarms, journal entries, COLOR, RSCALE |
| 4791, 6352, 6764 | CalDAV and CardDAV server, `.well-known` discovery |
| 4918, 7232 | WebDAV dead properties and ETag preconditions |
| 6578 | WebDAV collection sync (client) |
| 3676, 5322 | `format=flowed`, References, Reply-To, signature delimiter |
| 6350, 6186 | vCard, SRV service discovery |
| WebAuthn | Passkey session lock |

## Install

Linux binaries (`amd64`, `arm64`, `armv7`), both complete (`alborz`) and without
add-ons (`alborz-minimal`), along with macOS and Windows desktop packages, are
attached to every [release]. See [INSTALLATION.md](INSTALLATION.md) for a
systemd unit, reverse-proxy setup and building from source.

Container images are published at `ghcr.io/mehdisadeghi/alborz` as `:latest`
(or `:0.18.0`) and `:minimal` (or `:0.18.0-minimal`):

    docker run -p 1323:1323 -v alborz-data:/data ghcr.io/mehdisadeghi/alborz

`/data` holds both `config.toml` (including the generated login key) and
`alborz.db`; keep the volume across deployments to preserve encrypted data and
remembered sign-ins.

## Usage

Start without upstream arguments to configure a new instance in the browser:

    alborz

On first start, Alborz generates a login key in its configuration file and
prints a `/setup` URL and one-time setup code to the console. Open `/setup` to
discover and verify your mail, DAV and Sieve servers and save them to the
configuration file.

Configuration defaults to `alborz/config.toml` in the OS user configuration
directory (`$XDG_CONFIG_HOME` or `~/.config` on Linux); override the path with
`-config` or `LBRZ_CONFIG` (`none` disables file loading). Precedence is
`LBRZ_*` environment variables, CLI flags, configuration file, then defaults.
See the [example configuration](internal/config/config.example.toml) for all
settings.

You can also pass domains or upstream servers on the command line.

With SRV DNS records set up (see [RFC 6186]):

    alborz example.org

With upstream servers named explicitly:

    alborz imaps://mail.example.org:993 smtps://mail.example.org:465

Multiple mail domains:

    alborz example.org example.com=imaps://mail.example.com example.com=smtps://mail.example.com

See [docs/cli.md](docs/cli.md) for all flags and URL schemes.

## Contributing

Run `make test` and `make lint` with the Go version in `go.mod` and Node.js.
Both targets run `make vuln` (`govulncheck`), which requires network access to
fetch the vulnerability database; `make test-js` runs the browser tests offline
without npm dependencies.

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
