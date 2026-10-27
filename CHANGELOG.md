# Changelog

## [Unreleased]

### Added

- Choose full account addresses or stable color strips in multi-account lists;
  hiding addresses compacts the rows. Touch and keyboard reveal each strip's
  full address. Single-account records show neither.
- A header lock action that preserves signed-in accounts and coordinates
  concealment across open tabs, including restored and disconnected pages,
  counting user scrolling and input within HTML email as activity.
- Owner-only first-run setup with DNS discovery, verified TLS connections and
  a separate login step; structured TOML configuration, `LBRZ_` overrides and
  automatic encryption-key creation for fresh installations.

### Fixed

- Align mobile list stars and dates, including undated tasks and contacts;
  keep quick-add controls at 32px and sidebar addresses on one line.

### Changed

- Use the mobile drawer’s account rows for switching, with a color legend and
  a direct Settings action. Put sign-out in account settings and
  language, shortcuts and the tour in general settings.
