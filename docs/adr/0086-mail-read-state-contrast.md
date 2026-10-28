# ADR 86: Mail read-state contrast

Status: accepted. Amends ADR 77.

## Context

In seasonal themes, read and unread message subjects were hard to
distinguish at a glance. WCAG 2.2 SC 1.4.1 requires a cue beyond colour,
and SC 1.4.3 requires normal text to have at least 4.5:1 contrast
against its background.

## Decision

1. Mail lists MUST distinguish unread messages by font weight and row
   surface in every theme, never by colour alone. Read and unread
   subjects MUST use the same text colour.
2. Subject text MUST maintain at least 4.5:1 contrast against its actual
   composited row surface in both light and dark schemes, including
   translucent surfaces over seasonal scenes.

## Consequences

- Read state is carried by weight and row background while subject
  colour stays uniform.
- Theme changes must verify contrast over both schemes and seasonal
  surfaces.

## References

- WCAG 2.2 SC 1.4.1 (Use of Color), SC 1.4.3 (Contrast Minimum)
- ADR 77
- `templates/assets/style.css`, `addons/season/web/assets/themes/`
