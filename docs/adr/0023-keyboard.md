# ADR 23: Keyboard shortcuts and swipe actions

Status: accepted, amended by ADR 69 (`/` and `Ctrl-K`/`⌘-K` open the
cross-section search dialog) and ADR 75 (`g j` opens Journal).

## Context

List navigation, triage actions and section switching need consistent
keyboard shortcuts on desktop and touch swipe actions on mobile without
page-specific scripts.

## Decision

### Keys

1. Keyboard shortcuts follow one fixed scheme:
   - `j` / `k`: next and previous row or object;
   - `o` or `Enter`: open; `x`: select; `s`: star;
   - `/` or `Ctrl-K` / `⌘-K`: open the search dialog (ADR 69);
   - `e`: archive; `#`: delete; `!`: junk / not junk;
   - `c`: create / compose; `u`: up to list; `r`, `a`, `f`: reply, reply
     all, forward;
   - `g` then `m`, `l`, `c`, `k`, `j`, `f`, `s`: go to mail, calendar,
     contacts, tasks, journal, filters, starred;
   - `g` then `p`, `n`: previous and next page;
   - `?`: open the keyboard shortcut dialog.
2. `keys.js` has no page-specific logic: `data-key` binds a single key
   to a control, and `data-go` binds a `g <key>` chord.
3. With no row selected, an action key (`e`, `#`, `!`) selects the
   focused row before submitting.
4. Keyboard cursor state is native browser focus on the row's main link
   (`.list-row:has(a:focus-visible)`).
5. Shortcuts MUST be ignored while typing in an input or editor, during
   IME composition, while a modal dialog or popover is open, or when
   `Ctrl`, `Meta` or `Alt` is held (except `Ctrl-K` / `⌘-K`, ADR 69).

### Swipe

6. Touch swipe gestures on list rows trigger the configured row action
   (defaulting to delete `#` away from the reading edge and junk `!`
   toward it in mail, mirrored in RTL) and use the toolbar's form,
   notice and undo flow.

## Consequences

- Adding a shortcut to any control requires only a `data-key` or
  `data-go` attribute in its template.

## References

- ADR 69, ADR 75
- `templates/assets/keys.js`, `templates/partials/toolbar.html`
  (`icon-key`, `object-step`, `pager-link`), `templates/partials/nav.html`,
  `templates/assets/style.css`, `gestures.go`
