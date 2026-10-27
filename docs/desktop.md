# Desktop launcher

Alborz Desktop runs the local server and opens its browser interface. On macOS it
stays in the menu bar; on Windows it stays in the notification area. **Open Alborz**
returns to the browser. **Quit Alborz** stops the local server. The normal
`cmd/alborz` command remains the headless server.

Closing the browser leaves Alborz running; launching again opens the same instance.
The desktop server listens only on
`127.0.0.1:1323`, with the browser address `http://localhost:1323`. A conflicting
process on that port produces an explicit startup error.

First launch shows a one-time setup code. Copy it into the browser setup page.
**Copy setup code** remains in the native menu until setup finishes.

## Data and recovery

Windows keeps its data in `%LOCALAPPDATA%\Alborz`; macOS uses
`~/Library/Application Support/Alborz`. The directory contains `alborz.db`, the
`cache` directory, `alborz.log`, and the running-instance descriptor.

The desktop and headless server use the same TOML configuration: by default,
`%APPDATA%\alborz\config.toml` on Windows and
`~/Library/Application Support/alborz/config.toml` on macOS. Select another file
with `-config` or `LBRZ_CONFIG`. Settings follow the same precedence:
`LBRZ_*` environment, explicit command-line flags, TOML, then defaults.
The desktop accepts `addr`, `data-dir`, `login-key`, `cache`, `debug`,
`private-services`, `project-url`, and mail server settings. Its address must
remain IPv4 loopback; its public URL always names the local listener.

First launch generates the encryption key in the configuration file with mode
`0600`; setup saves mail server settings to that same file. An existing database
without its key causes an error. Back up the configuration together with the data
directory: recovery needs both. The Windows data directory retains its per-user
access controls. Portable ZIPs still use these data directories.

## Build

Use the Go version in `go.mod`, Python 3.8 or newer, and GNU Make:

```sh
make -f Makefile desktop-windows DESKTOP_ARCH=amd64
make -f Makefile desktop-windows DESKTOP_ARCH=arm64
make -f Makefile desktop-macos DESKTOP_ARCH=arm64
make -f Makefile desktop-macos-dmg DESKTOP_ARCH=amd64
```

Windows builds can be cross-compiled. macOS builds require macOS 13 or newer and
Xcode Command Line Tools; choose `amd64` for Intel or `arm64` for Apple silicon.
Outputs go to `dist/`; override with `DESKTOP_OUT`. Set `DESKTOP_VERSION` to the
numeric release version.

Each ZIP has a SHA-256 sidecar. On macOS, move `Alborz.app` from the ZIP or DMG
to `~/Applications` or `/Applications`. On Windows, unzip and run `Alborz.exe`.

Windows icon embedding downloads the pinned `github.com/akavel/rsrc` build tool.
Native icons derive from `templates/assets/icon-512.png`.

For a per-user Windows installer, install Inno Setup 6.3 or later and build on Windows:

```sh
make -f Makefile desktop-windows-installer DESKTOP_ARCH=amd64 ISCC="C:/Program Files (x86)/Inno Setup 6/ISCC.exe"
```

The installer needs no administrator privileges. It uses the user's Programs
directory, adds a Start menu shortcut and optionally a desktop shortcut.
Uninstalling preserves mail data and configuration; launch at login is not enabled.

Version tags (`vX.Y.Z`) build both desktop architectures and attach ZIPs, DMGs
and Windows installers to the same GitHub release as the Linux binaries.
Desktop packaging also runs when the build workflow is started manually; it
does not run on ordinary pull requests.

## Signing and validation

Artifacts are unsigned by default. Set `DESKTOP_SIGN_IDENTITY` for macOS Developer
ID signing with the hardened runtime and bundle verification. Notarization and
stapling remain separate steps, as does Windows Authenticode signing.

Before distributing, test each OS and architecture: first launch and setup, tray/menu
actions, code copying, browser opening, second launch, restart with existing data,
Quit, installer upgrade/uninstall, and configuration-file permissions.

The native menu and setup dialog currently use English. The browser interface
uses Alborz's existing language settings.
