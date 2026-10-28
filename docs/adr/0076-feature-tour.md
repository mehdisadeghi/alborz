# ADR 76: Feature tour

Status: accepted. Amends ADR 52 (`Profile.Toured`).

## Context

First-time readers meet six sections, a rail, an overflowing toolbar,
keyboard shortcuts, stars, undo notices and multi-account scope without
onboarding. The tour must work across every supported language and both
text directions, on narrow viewports, and with or without JavaScript.

## Decision

### Stops

1. One tour list in `web/tour.go` covers the shared layout (`sections`,
   `search`, `rail`, `accounts`, `toolbar`, `rows`, `notices`, `keys`,
   `scheme`), then each section (`mail`, `calendar`, `tasks`,
   `contacts`, `journal`, `filters`, `settings`), bracketed by `welcome`
   and `done`.
2. Each stop is data: a name, a target mark and an optional route. Title
   and body come from `tour.<name>.title` and `tour.<name>.body` in the
   locale catalogs.
3. Controls declare targets with `data-tour="<mark>"`. `tour.js` has no
   page-specific selectors.
4. A stop points at the first visible element carrying its mark in
   document order. On narrow viewports where rail controls are inside the
   closed drawer, the drawer button carries their marks and acts as the
   target. A stop without a visible target renders centred.
5. A stop with a route opens on that route in the current account scope;
   a stop without a route opens on the current page.

### State

6. The active stop is the `tour` query parameter. Back and Next are
   links to the neighbouring stop's URL, so the tour works without
   JavaScript.
7. With JavaScript, stepping to a stop on the same page swaps in place
   and replaces the URL; stepping to another page uses boosted
   navigation. Closing removes `tour` from the URL.
8. Page-generated navigation links (`next`, `from`, back links, language
   switcher) MUST strip the `tour` parameter.
9. `Profile.Toured` (ADR 52) is stored across all signed-in accounts.
   Sign-in opens `?tour=welcome` when `Toured` is false and sets
   `Toured` to true in the same request.
10. "Take the tour" in the account menu opens `?tour=welcome` on the
    current page at any time.

### Presentation

11. The `tour` partial renders a non-modal `<dialog open>` in the header
    with step position, title, body, Back, Next/Done and dismiss
    controls.
12. Positioning uses CSS Anchor Positioning (`anchor-name`,
    `position-area`, `position-try-fallbacks`), with a script fallback
    when unsupported and a bottom sheet at `<= 600px`. The target is
    ringed in `--accent` and scrolled into view.
13. While the tour dialog is open, Escape closes, inline-end/start
    arrows step Next/Back (mirrored in RTL), Enter on no control steps
    Next, and page shortcuts are suppressed (ADR 23 rule 7).

## Consequences

- The tour is offered automatically once per profile on first sign-in
  and remains available on demand from the account menu.
- Removing a `data-tour` mark from a template centres that stop rather
  than failing rendering; route renames are caught by the tour walk
  test.

## References

- ADR 13, ADR 23, ADR 41, ADR 52, ADR 55
- `web/tour.go`, `templates/partials/tour.html`,
  `templates/assets/tour.js`, `templates/assets/style.css` (`.tour`),
  `auth/web/login.go` (`loginRedirect`), `profile.go` (`Toured`),
  `locales/*.toml` (`tour.*`)
