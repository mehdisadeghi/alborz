# ADR 41: Notice layer

Status: accepted. Amends ADR 4.

## Context

A notice in the page flow moves the page when it appears and when it
is dismissed. Forms drew their own refusal alerts from per-page
`Error` fields, and dismissing a notice required script.

## Decision

1. Every notice (an outcome, a form refusal, offline or slow, a request
   without answer, an import in progress) MUST be drawn by the one
   `notice` partial inside the one `notices` layer.
2. The layer is `position: fixed` at the foot of the window, above
   the status bar where it shows, out of the page's flow; the nav,
   toolbar and crumb at the top stay uncovered. Nothing on the page
   moves when a notice appears or leaves.
3. A handler that re-renders a form with a refusal MUST set the page's
   notice to a failed one (`BaseRenderData.Refused`). Pages MUST NOT
   carry an `Error` field of their own.
4. A notice is a `<dialog open>`. Its dismiss button sits in a
   `method="dialog"` form, so dismissing needs no script.
5. A notice enters and leaves in 120 ms (slide of a few pixels plus
   fade). `prefers-reduced-motion` removes the motion.
6. A `done` notice leaves after 6 s; the countdown pauses while it is
   hovered, holds focus, or is pressed. `warning` and `failed` notices
   stay until dismissed.
7. A failed notice has `role="alert"`; others `role="status"`.

## Consequences

- A refusal is read at the top of the page, not beside the field; the
  form keeps its input.
- A shown notice covers what is under it until dismissed or expired.
- Statements that are page content (what deleting a folder takes with
  it) are not notices.

## References

- ADR 4, ADR 42
- `templates/partials/notice.html` (`notice`, `notices`),
  `templates/partials/nav.html`, `templates/assets/style.css`
  (`.notices`, `.notice`, `.notice-done`), `templates/assets/helpers.js`
  (`noticeLinger`), `web/notices.go`, `web/renderer.go` (`Refused`)
