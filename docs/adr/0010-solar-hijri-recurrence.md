# ADR 10: Solar Hijri recurrence and reckoning

Status: accepted, amended by ADR 73 (unreadable calendar data). Extends
ADR 9.

## Context

A Gregorian yearly rule anchored on 21 March drifts off 1 Farvardin in
the years the equinox moves. RFC 7529 lets a rule count months and
years in a named calendar (RSCALE) with SKIP for days a month lacks.
Few servers expand RSCALE.

## Decision

### Rules

1. The event form offers daily, weekly, monthly and yearly repeats,
   with an optional last day (UNTIL, in DTSTART's value type).
2. With Solar Hijri as the primary calendar, a monthly or yearly rule
   MUST be written with `RSCALE=PERSIAN;SKIP=BACKWARD`. Daily and weekly
   rules carry no RSCALE.
3. A rule the form cannot express (INTERVAL > 1, COUNT, any BYxxx, or a
   monthly or yearly rule in another scale) is shown as custom and kept
   unchanged.

### Expansion

4. Every rule is expanded by go-recur over the displayed window:
   RRULE with RSCALE and SKIP, RDATE, EXDATE, and overridden instances
   (RECURRENCE-ID, including RANGE=THISANDFUTURE). The go-ical fork
   reads a component and its overrides as one series; the go-webdav
   fork matches calendar-query time ranges on the same instances.
5. A series that cannot be expanded MUST show each component once, at
   its own DTSTART. A wrong instance is worse than a missing one.
6. alborz's own CalDAV store advertises CALDAV:supported-rscale-set
   with GREGORIAN and PERSIAN.

### Writes

7. Every PUT of `text/calendar` passes one check in the account's DAV
   transport. The RSCALE values of the new object, minus those of the
   object it replaces (a GET first, unless `If-None-Match: *`), MUST
   each be listed in the target calendar's supported-rscale-set.
   An empty set allows any scale. A calendar that sends no set, or is
   missing from the listing, allows none (RFC 7529 10).
8. A refused write fails with `ErrScaleUnlisted` and is shown as a form
   refusal or notice with one reason (`form.repeatscale`).

### Reckoning

9. Solar Hijri years are reckoned by Borkowski's algorithm (1996) in
   go-recur's `rscale` package, valid for years -61 to 3177. `calsys`
   holds only display concerns.
10. A date outside that range is shown in Gregorian, and its secondary
    Solar Hijri gloss is omitted.
11. Every date field carries the name of the calendar it is written in,
    in a hidden field: the primary calendar, or Gregorian for a stored
    date the primary cannot count. A field MUST be read in the calendar
    it names, never by the shape of its value.
12. Input outside the range is refused: a typed Solar Hijri date, and a
    month page whose neighbouring years are not reckoned.

## Consequences

- A Solar Hijri monthly or yearly repeat is saved only to a calendar
  whose set lists PERSIAN or is empty.
- An event stored by another client with an RSCALE rule stays editable
  anywhere, since the edit adds no scale; copying it to a calendar
  without the set is refused.
- Replacing an object with an RSCALE rule costs one extra GET.
- A server that accepts an empty set but does not expand the rule
  shows the event once in its own views.
- A new calendar system needs a calendar in go-recur's `rscale` and a
  `calsys.System`.

## References

- RFC 5545 3.3.10, 3.8.5; RFC 7529 (RSCALE, SKIP, 10);
  RFC 4791 5.2.3, 9.9
- ADR 9
- `pim/calendar/recurrence.go` (`FormRepeat`, `SetRepeat`, `plain`),
  `pim/calendar/caldav.go` (`Occurrences`), `pim/dav/scales.go`
  (`scaleGuard`, `ErrScaleUnlisted`), `pim/store/caldav.go`,
  `internal/calsys/calsys.go` (`For`, `ParsePage`),
  `internal/dates/field.go` (`Field`)
- `github.com/alborzmail/go-recur` (`rscale/persian.go`),
  `github.com/alborzmail/go-ical`, `github.com/alborzmail/go-webdav`
