# ADR 75: Journal entries

Status: accepted, amended by ADR 88 (moving entries between journals).
Amends ADR 49, ADR 63 and ADR 69.

## Context

A `VJOURNAL` (RFC 5545 3.6.3) is a dated note (`DTSTART`, `SUMMARY`, one
or more `DESCRIPTION`, `CATEGORIES`, `STATUS`, `CLASS`, `COLOR`) kept in
a CalDAV collection whose `supported-calendar-component-set` includes
`VJOURNAL` (RFC 4791 5.2.3). Clients such as Thunderbird, jtx Board and
Evolution create them; alborz needs a section to read and edit them.

## Decision

### Collections

1. Journal entries live in the account's calendar collections, read
   through the same CalDAV provider, mirror (ADR 53) and warm-up as
   tasks. A collection is a journal when its component set includes
   `VJOURNAL`; a collection with no component set is events only (ADR 49
   rule 5).
2. When no collection supports `VJOURNAL`, the section renders empty
   with "New journal". A collection created from the journal rail has
   `VJOURNAL` as its sole component.
3. A collection's settings page belongs to the first of calendar, tasks
   and journal that its component set lists.
4. Visible journals in the merged view are stored in CalDAV settings
   (`JournalFilter`, `VisibleJournals`, ADR 80).

### Pages

5. The section is `/journal` (`calendar.journal`) and an entry is
   `/journal/{path}` (`calendar.entry`), placed in the primary nav after
   Tasks (`g j`). List, entry, create, edit, delete, colour, source,
   copy, move (ADR 88), import, export and refresh share the task routes
   and handlers parameterized by component.
6. List columns are star, title, journal, date and added, sorted newest
   first by default (`pim.Column.Desc`). Search matches `SUMMARY`, every
   `DESCRIPTION` and `CATEGORIES`; views are Starred and the flag
   colours (`web.StarFilter`).
7. The date is `DTSTART` written as a `DATE`. An edit that preserves the
   day keeps an existing `DATE-TIME`; clearing the field removes
   `DTSTART`. Recurrence rules are not expanded.
8. The entry page displays status, categories and every `DESCRIPTION`.
   The editor edits title, date and text (joining multiple `DESCRIPTION`
   properties with a blank line and saving as one), while preserving
   `STATUS`, `CATEGORIES` and `CLASS`.
9. Dated notes and quick-add are not offered on journal entries.

### Search, stars and export

10. Journal registers a `web.Searcher` of kind `journal` answering `is`
    after tasks (ADR 69).
11. Stars use `COLOR` (RFC 7986 5.9) via `pimweb.Star` with undo and
    notices matching tasks.
12. Collection export and UID lookup on import include `VJOURNAL`
    alongside `VEVENT` and `VTODO`, supplying a missing `DTSTAMP` on
    export.

## Consequences

- Warming an account runs one additional query per journal collection.
- A collection supporting both events and journals appears in both
  rails.
- Editing an entry with multiple `DESCRIPTION` properties consolidates
  them into one.

## References

- RFC 5545 3.6.3, 3.8.1.11, 3.8.2.4; RFC 4791 5.2.3; RFC 7986 5.9
- ADR 47, ADR 49, ADR 53, ADR 63, ADR 69, ADR 80, ADR 88
- `pim/calendar/caldav.go` (`SupportsJournal`, `FirstJournal`,
  `JournalQuery`), `pim/calendar/settings.go`,
  `pim/calendar/web/journal.go`, `pim/calendar/web/entry.go`,
  `templates/pages/journal/`, `templates/partials/journal/row.html`
