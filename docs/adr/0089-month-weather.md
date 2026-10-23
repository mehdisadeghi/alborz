# ADR 89: Weather in the month grid

Status: accepted.

## Context

The weather service caches a station's complete hourly MOSMIX forecast, but
only its current hour appeared in the status bar. Calendar planning also needs
a compact indication on upcoming days.

## Decision

- Reuse the selected place and existing weather status setting. Disabling
  weather or forgetting the place removes the month annotations.
- Add a generic month-only day-annotation hook using the existing page
  extension mechanism. Core calendars do not import the weather add-on.
- Load annotations separately with HTMX and refresh hourly. Background
  requests carry `Alborz-Unasked`, so they do not keep an idle account alive.
  A weather fetch must never hold up the month page.
- Reuse the station's cached forecast for every visible date. Today uses the
  current forecast hour; future dates use the first available hour at or after
  local noon, on that same date. Tooltips name the actual forecast hour.
- Render only available conditions, with localized accessible descriptions.
  Past dates, missing values and dates beyond the forecast remain empty.
  Empty out-of-band updates clear obsolete icons after settings or data change.
- Keep a compact annotation row separate from primary and secondary dates and
  week numbers. Clicking an icon opens the same day with the same account and
  calendar scope as the cell's normal link.

## Consequences

These icons summarize a forecast hour, not a daily aggregate. The tooltip makes
that distinction explicit. No new weather provider, per-day download, or user
preference is required. The calendar remains usable when weather is unavailable
or excluded from the build.
