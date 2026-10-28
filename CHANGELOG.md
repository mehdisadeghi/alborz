# Changelog

## [Unreleased]

### Added

- An account-menu lock action that preserves signed-in accounts and coordinates
  concealment across open tabs, including restored and disconnected pages,
  counting user scrolling and input within HTML email as activity.
- Owner-only first-run setup with DNS discovery, verified TLS connections and
  a separate login step; structured TOML configuration, `LBRZ_` overrides and
  automatic encryption-key creation for fresh installations.
