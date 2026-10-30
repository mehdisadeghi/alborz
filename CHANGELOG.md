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
- A focus link fills the window with the writing area in mail and journal
  entries; Back leaves it.

### Fixed

- Page links in mail subfolders keep the folder path.
- Collection columns are capped so summaries take the free space; the task list
  shows Added before Due date and keeps Due date longest on narrow windows.
- New mail, events, tasks, journal entries and contacts preselect the account
  in view.
- A page opened from the sidebar takes its own layout, so toolbars no longer
  keep the previous form page's margins.
- Align mobile list stars and dates, including undated tasks and contacts;
  keep quick-add controls at 32px and sidebar addresses on one line.
- Every sidebar shows All accounts and each account with its own Starred, as
  mail does, and lists its actions after all items; on touch the account
  chevron is a target of its own. Header icons share one spacing, form actions
  sit on one row or stack together, and account pickers read left to right.
- Dates and counts filled into translated labels use the reader's digits.
- Sidebar and page links keep a folder name that holds a slash.
- Recipient suggestions come from every signed-in account's address books, not
  only the account in view.
- Enter in a compose field sends instead of applying the signature; upload
  rows, upload errors and the draft notice are translated.

- Mobile navigation reserves its primary action’s space, accounts for safe-area
  insets, and groups less frequent sections under More on narrow layouts.
- Passkey setup remains reachable when mobile bottom navigation is enabled.
- Keyboard dismissal restores mobile navigation even while an editor retains
  focus. Overflow menus remain reachable in short viewports and without CSS
  anchor positioning; selected sections have a visible indicator.

### Changed

- A task's parent is set in its edit form; the detail page lists its subtasks
  and no longer offers Add subtask.
- The “Sent by Alborz Webmail” line is pre-filled, translated and editable under
  the signature in new mail, replies and forwards instead of added at sending.
- Browser history follows the markup: page navigations add an entry, list
  refinements replace it, and done, star and colour update the page in place.
- Cc, Bcc and Reply to share one section in compose, open when any holds an
  address. Files dropped anywhere on the page attach, the hint showing over the
  writing area; Ctrl+A on a message selects only its body.
- Use the mobile drawer’s account rows for switching, with a color legend and
  a direct Settings action. Put sign-out in account settings and
  language, shortcuts and the tour in general settings.

### Security

- Listeners use plain TCP instead of Go's default Multipath TCP on Linux,
  closing the published remote code execution path of CVE-2026-98432.
