# Changelog

## [Unreleased]

### Added

- Choose full account addresses or stable color strips in multi-account lists;
  hiding addresses compacts the rows. Touch and keyboard reveal each strip's
  full address. Single-account records show neither.
- Windows and macOS desktop launchers with native menus, per-user local instances,
  browser-based first-run setup, and portable/per-user installation packages.
- A header lock action that preserves signed-in accounts and coordinates
  concealment across open tabs, including restored and disconnected pages,
  counting user scrolling and input within HTML email as activity.
- Owner-only first-run setup with DNS discovery, verified TLS connections and
  a separate login step; structured TOML configuration, `LBRZ_` overrides and
  automatic encryption-key creation for fresh installations.

### Fixed

- Page links in mail subfolders keep the folder path.
- New mail, events, tasks, journal entries and contacts preselect the account
  in view.
- A page opened from the sidebar takes its own layout, so toolbars no longer
  keep the previous form page's margins.
- Align mobile list stars and dates, including undated tasks and contacts;
  keep quick-add controls at 32px and sidebar addresses on one line.

- Mobile navigation reserves its primary action’s space, accounts for safe-area
  insets, and groups less frequent sections under More on narrow layouts.
- Passkey setup remains reachable when mobile bottom navigation is enabled.
- Keyboard dismissal restores mobile navigation even while an editor retains
  focus. Overflow menus remain reachable in short viewports and without CSS
  anchor positioning; selected sections have a visible indicator.

### Changed

- Browser history follows the markup: page navigations add an entry, list
  refinements replace it, and done, star and colour update the page in place.
- Use the mobile drawer’s account rows for switching, with a color legend and
  a direct Settings action. Put sign-out in account settings and
  language, shortcuts and the tour in general settings.
