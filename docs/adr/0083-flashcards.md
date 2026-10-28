# ADR 83: Flashcards

Status: proposed. Depends on ADR 82 (`noflashcards`) and ADR 84
(dictionary).

## Context

Readers learning a language encounter unfamiliar words in mail and want
to capture a word with its source sentence for spaced-repetition review,
with two-way Anki deck exchange.

## Decision

1. A card holds front, back, source sentence, source message reference
   (account, folder, UID), language pair (BCP 47 tags), tags, creation
   time and review state.
2. Cards belong to per-account decks stored in `alborz.db` (ADR 51) and
   sealed like other account data (ADR 70, ADR 72).
3. Each deck selects a scheduler: SM-2 or FSRS (using published default
   parameters). Both are pure functions of card state, grade (`again`,
   `hard`, `good`, `easy`) and review time; review logs are preserved so
   switching schedulers derives FSRS state from history.
4. Message pages offer "Add card" (pre-filling front from selection,
   sentence from context, and back from the first dictionary sense when
   ADR 84 matches, with a plain HTML form fallback).
5. Review presents one due card per page with keyboard and form-POST
   grading; an optional status bar segment shows the due count.
6. Decks import and export CSV and Anki `.apkg` archives, preserving
   review logs.
7. Front and back carry independent text direction (`dir`).

## Consequences

- `.apkg` import/export requires a vetted pure-Go SQLite reader.
- Packaged as an optional add-on under `addons/flashcards` (ADR 82).

## References

- P. Wozniak, SuperMemo 2 algorithm (1987)
- FSRS specification: https://github.com/open-spaced-repetition
- ADR 51, ADR 70, ADR 72, ADR 82, ADR 84
