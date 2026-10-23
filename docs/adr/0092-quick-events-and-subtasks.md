# ADR 92: Quick events and subtasks

Status: accepted.

## Decision

- Calendar quick-add uses the task quick-add destination rules: a single
  selected collection is named explicitly, otherwise a picker is shown.
  Persistently hidden collections are excluded; temporary list filters do not
  erase an account's available destinations.
- Parse documented Gregorian dates and localized today/tomorrow expressions,
  with optional local clock times or time ranges. Normalize localized digits.
  Date-only entries are all-day; a lone clock time lasts one hour.
- Reject ambiguous or nonexistent daylight-saving times and preserve unclear
  input in the full event form rather than silently guessing.
- Subtasks are VTODOs related by `RELATED-TO;RELTYPE=PARENT`. Creation verifies
  that the parent belongs to the writable destination collection. Parent and
  child navigation includes completed children; completion never cascades.

## Consequences

This is predictable parsing, not unrestricted natural-language understanding.
The placeholder and hint show supported input. The evaluated dateparse and
when packages introduced ambiguity or lacked the app's supported languages,
so no parser dependency or external text service is added.

References: RFC 5545 section 3.8.4.5; github.com/araddon/dateparse;
github.com/olebedev/when.
