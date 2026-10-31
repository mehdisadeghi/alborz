# Changelog

## [Unreleased]

## [0.20.0] - 2026-10-31

### Added

- French and Arabic interface languages.
- A header language menu, and a person menu listing the accounts, All
  accounts, Add account, Settings, Keyboard shortcuts, the tour and Sign out
  of all accounts.
- Signatures, Servers, Sessions and Notifications list every signed-in
  account's rows under All accounts; a new signature there asks for its account.
- Windows and macOS desktop launchers with native menus and installation
  packages; an owner-only first-run setup with DNS discovery and verified TLS,
  TOML configuration with `LBRZ_` overrides and automatic encryption keys.
- A header lock action that keeps accounts signed in and hides every open tab,
  counting scrolling and input within HTML email as activity.
- Choose full account addresses or stable color strips in multi-account lists;
  hiding addresses compacts the rows. Single-account records show neither.
- An optional phone bottom bar with the sections and the page's primary
  action; less frequent sections sit under More.
- Configurable swipe actions on list rows, and swipes between pages and
  calendar periods; deleting anything but mail by swipe asks first.
- Copy and move events, contacts, tasks and journal entries between
  collections and accounts.
- Calendar quick add understands everyday date phrases in each language.
  Tasks take subtasks: the parent is set in the edit form, and the task page
  lists its subtasks.
- Weather icons for the coming days in the calendar month view.
- Mail notifications show a preview with Mark read and Trash actions and can be
  turned on per account; event reminders name the event.
- A preferred start page.
- A focus button fills the window with the writing area in mail and journal
  entries; the minimize button or Back leaves it.
- Alborz instances announce themselves to each other through the mail
  server, so collections kept on another Alborz are found there.

### Changed

- Calendar, contact, task and journal rails group collections under a heading
  for each place they are kept; the account heading opens all its collections
  and replaces the All row. Breadcrumbs name All accounts or the account,
  never both.
- Calendar views have a title row above the toolbar; the list view always
  shows the whole month, and quick add is offered on the list and day views only.
- The header holds only icon buttons; the system colour scheme has a sun-moon
  icon. The phone drawer holds the account rows with their color legend,
  Settings and Sign out of all accounts. Signing out of a single account is on
  Sessions.
- The “Sent by Alborz Webmail” line is pre-filled, translated and editable under
  the signature in new mail, replies and forwards instead of added at sending.
- Cc, Bcc and Reply to share one section in compose, open when any holds an
  address. Files dropped anywhere on the page attach; Ctrl+A on a message
  selects only its body.
- Browser history: opening a page adds an entry, list refinements (filter,
  sort, page, view) replace it, and actions such as done, star and colour add none.
- The listening address is printed at start.

### Fixed

- Calendar and contact discovery uses the server a mail host's well-known
  address redirects to, and a server named by the account that is this Alborz
  is no longer listed twice.
- Passkeys are validated against the configured public URL behind reverse
  proxies; remembered sign-ins are renewed by activity.
- New mail, events, tasks, journal entries and contacts preselect the account
  in view; recipient suggestions come from every signed-in account's address books.
- Enter in a compose field sends instead of applying the signature. Upload
  rows, upload errors and the draft notice are translated, and dates and counts
  in labels use the reader's digits.
- Page links keep the mail subfolder path and folder names holding a slash;
  printed HTML mail paginates without clipping; mail search and starred views
  keep their sidebar.
- Hidden collections are no longer offered as destinations; ticking a calendar
  on the day page's rail works; imported and absolute alarms and long repeat
  spans fire at the right times.
- Every sidebar shows All accounts and each account with its own Starred, as
  mail does, and lists its actions after all items. Header icons share one
  spacing, form actions sit on one row or stack together, account pickers read
  left to right, and collection columns leave summaries the free space.
- On phones, list stars and dates align, the drawer closes after a rail tap in
  every section, the bottom bar returns when the keyboard closes, and overflow
  menus stay reachable in short viewports.

### Security

- Listeners use plain TCP instead of Go's default Multipath TCP on Linux,
  closing the published remote code execution path of CVE-2026-98432.
- HTML mail is parsed with golang.org/x/net v0.62.0, fixing a raw-text
  rendering flaw in the sanitizer and quadratic parsing of crafted text nodes
  (GO-2026-6866, GO-2026-6860).
