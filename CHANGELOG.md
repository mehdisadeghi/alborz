# Changelog

## [Unreleased]

### Added

- A preferred start page, optional pooled signatures and an optional Alborz
  attribution appended independently of signatures.
- Calendar quick-add with localized date expressions, and standard VTODO
  parent/child tasks with independent completion.
- Mail notification previews, mark-read/trash actions and validated message links.
- Event titles in calendar reminder notifications.

### Changed

- Update the go-vcard, go-itip and go-webdav forks, preserving atomic DAV
  deletion checks for both ETag preconditions.

### Fixed

- Hidden DAV collections no longer appear as creation destinations.
- Compose preserves the selected account across entry points.
- Inline and remote mail image handling, including CSS background images.
- Mobile tour suppression, theme-color updates and seasonal animation layering.
- PWA navigation no longer waits on cache storage before starting its request.

### Security

- Added regression coverage for session locking across tabs and routes, and
  bounded notification previews and stale mailbox notification links.
