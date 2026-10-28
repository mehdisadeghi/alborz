# ADR 67: Status bar and compiled-in add-ons

Status: accepted, amended by ADR 77 (season add-on) and ADR 82 (add-ons
as self-contained build-tagged packages).

## Context

Unread mail counts, today's events, current dates in primary and
secondary calendars, and optional status items (moon phase, weather,
text of the day) need a compact, non-intrusive bar at the bottom of the
viewport and a clean extension seam for add-ons.

## Decision

### Status bar

1. Features and add-ons register `web.Segment{Name, End, Page, Show}` on
   `web.Server.Status` in mount order. `Show` returns
   `web.Shown{Parts, Title, Link}`; the core renders each part in its
   own `<bdi>`, joined by ` · `, linked when `Link` is set.
2. The segment name owns `/status/{name}` (`core.status`), the profile
   list entry (`Profile.Display.Status`) and the locale key
   `<name>.status`.
3. All segments are off by default and toggled individually in Settings.
   Unknown stored segment names are preserved (ADR 26).
4. Each enabled segment loads via htmx on page load, every 60 s, and
   after boosted navigation under a 2 s server deadline (`statusWait`).
   Timeouts and empty results render a dash; errors keep the previous
   value.
5. Segment requests carry `Alborz-Unasked`: they MUST NOT count as
   session activity, show the loading bar, or redirect on locked/signed-out
   sessions (answering 204 instead).
6. Start segments flow from the inline start and truncate the last item
   with an ellipsis when full; `End` segments flow from the inline end
   starting with the primary calendar date.
7. The bar renders only when at least one segment is enabled, preserves
   segments across boosted navigations (`hx-preserve`), and is hidden at
   `<= 600px` and under `scripting: none`.
8. Built-in core segments are `mail` (inbox unread count), `calendar`
   (today's event summary and next start time), `date-gregorian` and
   `date-shcal`.

### Add-ons

9. Add-ons live under `addons/<name>` and `addons/<name>/web` and are
   mounted via build-tagged registration files in `cmd/alborz/app/`
   (ADR 82).
10. Page add-ons register `web.Addon{Name, Script, Settings}` on
    `web.Server.Addons`, toggled in `Profile.Display.Addons` under label
    `<name>.addon`.

## Consequences

- Adding a status segment requires only a `web.Segment` registration and
  its locale labels; `status_test.go` verifies every mounted segment is
  translated in all supported languages.

## References

- ADR 66, ADR 77, ADR 78, ADR 79, ADR 82
- `web/status.go`, `web/addons.go`, `templates/partials/status.html`,
  `mail/web/status.go`, `pim/calendar/web/status.go`,
  `cmd/alborz/app/status_test.go`
