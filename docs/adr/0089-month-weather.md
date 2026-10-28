# ADR 89: Weather in the month grid

Status: accepted. Amends ADR 79.

## Context

The weather add-on caches a 240-hour hourly MOSMIX forecast for the
reader's chosen place (ADR 79), but showed only the current hour in the
status bar. The calendar month view benefits from compact daily weather
icons on upcoming dates without coupling `pim/calendar` to the add-on.

## Decision

1. The calendar month page exposes a generic day-annotation extension
   hook via `web.Extend`; `pim/calendar` MUST NOT import
   `addons/weather`.
2. When the `weather` status segment is enabled and `Profile.Place` is
   set (ADR 79), the month grid loads day annotations asynchronously via
   htmx with `Alborz-Unasked` and refreshes them hourly. Weather fetches
   MUST NOT delay rendering the month page.
3. Annotations read the station's cached forecast: today uses the
   current forecast hour, and future dates use the first forecast hour
   at or after local noon on that date. Past dates and dates beyond the
   forecast range render empty.
4. Each icon carries a localized accessible label and tooltip naming the
   forecast hour, condition, temperature and DWD attribution, and links
   to that day's view in the cell's account and calendar scope.
5. Disabling the weather segment or clearing `Profile.Place` sends empty
   out-of-band updates that clear the annotations.

## Consequences

- Each day's icon represents a single forecast hour (current hour today,
  local noon on future days), stated in its tooltip.
- Builds with `-tags noweather` or `-tags noaddons` (ADR 82) omit the
  annotations without affecting the calendar.

## References

- ADR 66, ADR 67, ADR 79, ADR 82
- `addons/weather/web/calendar.go`, `pim/calendar/web/month.go`,
  `templates/pages/calendar/month.html`
