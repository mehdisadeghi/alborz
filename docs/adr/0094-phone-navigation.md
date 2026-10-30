# ADR 94: Phone navigation

Status: accepted.

## Context

On a phone the rail is a drawer behind a button, so moving between
sections takes two taps. A bottom bar is one tap away, but it takes
height from the page and must not cover the keyboard or a focused field.

## Decision

1. The bottom navigation is opt-in, set in the profile. Compact
   navigation MUST show Mail, Calendar, a reserved primary-action track,
   Tasks and More. More holds scoped links to Contacts, Journal and
   Filters and names the current child section.
2. At a container width of 24rem the bar MUST show all six
   destinations. Its container queries follow the reader's text-size
   preference; browsers without container queries keep the compact
   layout. The centre track holds the primary action and its spacing.
3. The bar's total height MUST include the safe-area inset. The current
   section MUST be marked by a visible rule as well as colour, and page
   links keep `aria-current`.
4. The bar MUST hide for keyboard occlusion only while an editable field
   is focused and the VisualViewport has lost substantial height. It
   MUST return when the viewport recovers, whatever has focus. Zoom MUST
   NOT hide it, and an orientation change resets the baseline.
5. Overflow menus MUST be bounded and scroll. Without CSS anchor
   positioning a menu is placed above or below its opener by the space
   available and clamped to the viewport.

## Consequences

- Keyboard detection is a conservative heuristic: browsers offer no
  universal keyboard API. Without VisualViewport the bar stays shown.
- A page first opened in an already resized Android viewport has no
  pre-keyboard baseline.
- Physical-device keyboard behaviour is not covered by automated tests.

## References

- ADR 13, ADR 35
- `templates/assets/phone.js`, `templates/partials/nav.html`,
  `templates/layout.html`, `web/profile.go`, `settings/web/profile.go`,
  `templates/assets/style.css` (`data-phone`)
- `tests/phone.test.cjs`, `tests/popover-place.test.cjs`
