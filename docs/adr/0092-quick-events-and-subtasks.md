# ADR 92: Quick events and subtasks

Status: accepted.

## Decision

- Calendar quick-add uses the task quick-add destination rules: a single
  selected collection is named explicitly, otherwise a picker is shown.
  Persistently hidden collections are excluded; temporary list filters do not
  erase an account's available destinations.
- Extract date, clock/range and duration independently, wherever they appear
  in a title. Support Gregorian ISO/dotted dates, localized today/tomorrow,
  English weekday and named-month dates, `in N days`, noon/midnight, spaced
  am/pm, time ranges and `for N minutes/hours`. Normalize localized digits and
  syntax punctuation while retaining original title text.
- Date-only entries are all-day. Clock-only entries use their next occurrence;
  weekdays always use the next occurrence, and named dates without a year use
  their next occurrence. Bare hours after `at` use 24-hour time. Timed entries
  default to one hour; a trailing meridiem applies to both clocks in a range.
- Reject conflicting dates/clocks, invalid dates, unsupported obvious timing
  and recurrence, and ambiguous or nonexistent daylight-saving times. Recognized
  unsupported timing modifiers produce an error rather than an all-day fallback.
- Show interpretation from the same server parser in a compact overlay while
  typing, with clocks first so they remain visible on narrow screens. Keep help
  collapsed and out of the calendar layout. Validation redraws the originating
  calendar with the draft, selected destination and filters intact, plus an
  explicit link carrying the draft and destination into the full editor.
- Subtasks are VTODOs related by `RELATED-TO;RELTYPE=PARENT`. Creation verifies
  that the parent belongs to the writable destination collection. Parent and
  child navigation includes completed children; completion never cascades.

## Consequences

This is predictable parsing, not unrestricted natural-language understanding.
The placeholder and expandable help show supported input. A bounded extraction
parser keeps title spans, range/duration rules, translation and daylight-saving
validation under one policy. The evaluated dateparse, when and go-naturaldate
packages do not supply that complete policy: flexible parsing alone does not
make extraction, ranges or ambiguous local clocks safe. No parser dependency
or external text service is added. English grammar additions are not translated
grammar; translated today/tomorrow and clock connectors remain supported.

References: RFC 5545 section 3.8.4.5; github.com/araddon/dateparse;
github.com/olebedev/when; github.com/tj/go-naturaldate.
