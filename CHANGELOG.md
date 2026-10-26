# Changelog

## [Unreleased]

### Added

- An account-menu lock action that preserves signed-in accounts and coordinates
  concealment across open tabs, including restored and disconnected pages,
  counting user scrolling and input within HTML email as activity.
- A preferred start page, optional pooled signatures and an optional Alborz
  attribution appended independently of signatures.
- Calendar quick-add with localized date expressions, and standard VTODO
  parent/child tasks with independent completion.
- Mail notification previews, mark-read/trash actions and validated message links.
- Event titles in calendar reminder notifications.

### Changed

- Update the go-ical and go-recur forks for calendar-day recurrence spans and
  all-day shifts across daylight-saving transitions.
- Update the go-vcard, go-itip and go-webdav forks, preserving atomic DAV
  deletion checks for both ETag preconditions.

### Fixed

- Keep sign-in feedback active until the destination loads and preserve the
  Remember me choice after a refused login.
- Replace list refinements, neighbouring objects and editor transitions in
  browser history so Back returns to the list without replaying those views.
- Interpret natural quick-event phrases, preview their date/time, and preserve
  unclear drafts and destinations inline with compact help.
- Keep recipient suggestions within the compose page and hide the empty drag-and-drop box on phones.
- Preserve the main mail body when a mailing list appends a separate footer.
- Authentication transitions refresh session metadata, and locked tabs conceal
  private content before navigation or network verification completes.
- Hidden DAV collections no longer appear as creation destinations.
- Compose preserves the selected account across entry points.
- Inline and remote mail image handling, including CSS background images.
- Mobile tour suppression, theme-color updates and seasonal animation layering.
- PWA navigation no longer waits on cache storage before starting its request.

### Security

- Reject malformed off-site return paths during authentication and unlocking.
- Added regression coverage for session locking across tabs and routes, and
  bounded notification previews and stale mailbox notification links.
