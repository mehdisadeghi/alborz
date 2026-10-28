# ADR 9: Calendar system is display only

Status: accepted, extended by ADR 10 (Solar Hijri recurrence and
reckoning).

## Context

A reader may count in Solar Hijri instead of Gregorian. Dates could be
converted at the storage boundary, or the same instants counted
differently where they are shown and entered.

## Decision

1. Stored events MUST remain iCalendar DATE or DATE-TIME values
   (RFC 5545). The calendar system never changes what is stored.
2. A calendar system is a `calsys.System`: day arithmetic from
   go-recur's `rscale`, the name alborz stores (`gregorian`, `shcal`),
   the locale key of its month names and the weekday its weeks start
   on. The known systems are Gregorian and Solar Hijri.
3. The reader chooses a primary and an optional secondary system
   (`Reading.Primary`, `Reading.Secondary`; empty means Gregorian and
   none). The renderer's date helpers count in the primary and gloss
   in the secondary.
4. The month view asks the primary system for the month's bounds and
   its URL name: `?month=YYYY-MM` is read in the primary system
   (`Page`, `ParsePage`).
5. The first day of the displayed week is the reader's own setting
   (`FirstDayOfWeek`, default Monday). The system's week start is used
   only to number weeks; Gregorian weeks are numbered by ISO 8601.
6. Month names come from the locale files (`calendar.gmonth.N`,
   `calendar.shmonth.N`).
7. On narrow screens a month's first day shows the bare day number,
   like every other cell.

## Consequences

- Switching the primary system changes nothing on the server; no event
  is rewritten.
- A date formatted through the renderer follows the choice; one
  formatted with `time.Format` does not.

## References

- RFC 5545 3.3.4, 3.3.5; RFC 7529; ISO 8601
- ADR 10
- `internal/calsys/calsys.go` (`System`), `internal/dates`,
  `web/renderer.go` (`Primary`, `Secondary`), `profile.go` (`Reading`),
  `pim/calendar/web/month.go` (`monthView`),
  `pim/calendar/web/grid.go` (`weekOf`),
  `templates/pages/calendar/month.html`
