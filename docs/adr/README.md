# Architecture decision records

Michael Nygard's ADR form (Context, Decision, Consequences), with rules
worded as in BCP 14 (RFC 2119, RFC 8174). An ADR records why alborz
chose something; it is not an IETF RFC (RFC 7322), which specifies a
protocol and has no place for the context or the cost of a choice.

One decision per file: `NNNN-short-plain-name.md`, numbered in order,
never renumbered; a gap stays a gap.

    # ADR N: Title
    Status: proposed | accepted | superseded by ADR M. Amends ADR K.
    ## Context        what the decision needs, a few lines
    ## Decision       numbered rules; MUST, SHOULD, MAY as in BCP 14
    ## Consequences   short bullets
    ## References     RFCs, ADRs, code paths

Rules state behaviour, not history. Use the wording of the RFCs a rule
rests on. When the code and an ADR disagree, one of them is wrong: fix
it in the same change. A new decision gets a new number; a changed one
amends or supersedes the old in its Status line.

Bugs and features, open or done, live in `docs/tracker.db`; an item
that needed a decision names its ADR there.
