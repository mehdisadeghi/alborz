# ADR 84: Dictionary

Status: proposed. Depends on ADR 82 (`nodictionary`).

## Context

Readers looking up a word need definitions or translations in a language
they know without leaking reading activity to third-party services by
default.

## Decision

1. Lookups MUST be offline by default against dictionary packs installed
   on the instance or imported per account. Each pack records its source
   and target languages, licence and attribution line, displayed with
   every entry.
2. Supported import formats are StarDict (`.ifo`, `.idx`,
   `.dict`/`.dict.dz`), TSV and Wiktionary `kaikki.org` JSON Lines.
   Export is TSV. Uploads are bounded like compose attachments (ADR 38).
3. When no direct language-pair pack exists, a chain lookup of at most
   two hops resolves translations through intermediate packs (Wiktionary
   translation tables, Wikidata lexemes, Wikipedia interlanguage links),
   naming each hop's source.
4. Online lookups (Wikidata, Wiktionary) MUST be disabled unless enabled
   by an operator flag, and MUST display the service name and privacy
   policy link before the first query (as in ADR 79).
5. Lookup results are cached in memory; packs are read-only after
   import.

## Consequences

- Coverage depends on installed packs; pivot lookups through English
  bridge sparse language pairs such as Persian-German.
- Packaged as an optional add-on under `addons/dictionary` (ADR 82).

## References

- StarDict format specification; FreeDict; `kaikki.org`; Wikidata
  Lexicographical data
- ADR 38, ADR 79, ADR 82, ADR 83
