# ADR 8: Toolbar overflow order

Status: accepted.

## Context

When a toolbar row narrows, controls must give up width. Viewport
breakpoints and a measuring script deciding independently caused
overlap and controls returning after they had left.

## Decision

1. One script decides. Viewport breakpoints MUST NOT hide, shorten or
   move toolbar controls, except that on a phone a view toggle shows
   only the view it leads to before the script measures.
2. Every control on the row is rigid; the title is the only elastic
   element. A row that does not fit overflows (`scrollWidth >
   clientWidth`) instead of overlapping.
3. Each element that can yield width declares `data-priority` (order)
   and `data-yield` (behaviour):
   - `hide`: hidden in place;
   - `word`: text label dropped, icon kept;
   - `split`: a control group (calendar previous, Today, next) whose
     members move into the overflow menu as individual labelled rows;
   - absent: moved into the row's overflow menu.
4. The script applies steps in ascending priority until the row fits,
   and restores them when it widens (`ResizeObserver`).
5. Priority order on every page:

       1   page range (hide)
       2   pager labels (word)
       3   view toggle labels (word)
       4   rows-per-page menu (hide)
       10+ page actions: download, delete, then the rest
       20  title, to its short form (word)
       25  filter menu
       30  sort and group-by
       31  calendar pager with Today
       32  calendar view toggle

6. The create action (`New`) has no priority and MUST NOT leave the row.
7. The title truncates only after every step has been taken.
8. Views of the same page (calendar month and list, empty and populated
   lists) render the same end-group controls, disabling any that do not
   apply so the end group stays aligned across views. Select-all appears
   only where rows have checkboxes.
9. A toolbar MAY put its title on a row of its own above the controls
   (`toolbar-head` with `titlerow`); the calendar month, list and day
   views do. That title is outside the measured row and takes its short
   form by a container query on its own width, never by a viewport
   breakpoint.

## Consequences

- Toolbar overflow behaviour is declared in markup; a new control joins
  by setting `data-priority`.
- Minimum row width is the title's floor plus the controls that never
  leave.

## References

- `templates/assets/helpers.js` (priority-plus toolbar),
  `templates/partials/toolbar.html`,
  `templates/partials/calendar/toolbar.html`,
  `templates/pages/calendar/day.html`,
  `templates/assets/style.css`
