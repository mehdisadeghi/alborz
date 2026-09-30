# ADR 94: Phone navigation

Status: accepted.

## Decision

- Keep the bottom navigation opt-in. Compact navigation shows Mail, Calendar,
  a reserved primary-action track, Tasks and More. More contains scoped links
  to Contacts, Journal and Filters and names the current child section.
- At a container width of 24rem, show all six destinations. Container queries
  follow the reader's text-size preference; unsupported browsers keep the
  compact layout. The center track includes the primary action and spacing.
- Add safe-area padding to the bar's total height. Identify the selected section
  with a visible rule as well as color, retaining `aria-current` on page links.
- Hide navigation for keyboard occlusion only when an editable field is focused
  and VisualViewport loses substantial height. Restore it when the viewport
  recovers, regardless of focus. Ignore zoom and reset after orientation changes.
- Bound and scroll overflow menus. Without anchor positioning, place menus above
  or below their opener according to available space and clamp to the viewport.

## Consequences

Keyboard detection is a conservative heuristic: browsers provide no universal
keyboard API. Without VisualViewport, navigation stays available. A page first
opened with an already-resized Android viewport lacks a pre-keyboard baseline.

Tests cover localized account scope, active child navigation, viewport changes,
keyboard dismissal and popover placement. Browser checks cover 320px layouts,
larger text, RTL, simulated safe areas and short screens with and without CSS
anchor positioning. Physical-device keyboard behavior still needs validation.
