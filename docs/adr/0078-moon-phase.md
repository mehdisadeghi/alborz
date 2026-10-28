# ADR 78: Moon phase add-on

Status: accepted, amended by ADR 82 (packaged under `addons/moon`).
Follows ADR 67.

## Context

Readers may want the current moon phase in the status bar without
external network requests or third-party dependencies.

## Decision

1. The add-on registers an `End` status bar segment named `moon`
   (ADR 67, ADR 82), off by default and toggled under Status bar in
   Settings (`moon.status`).
2. `addons/moon` computes the phase locally from the instant's age in
   the mean synodic month (29.530588853 days) anchored at the new moon
   of `2000-01-06 18:14 UTC`, divided into eight phases centred on new
   moon, first quarter, full moon and last quarter.
3. The segment displays the phase emoji (🌑🌒🌓🌔🌕🌖🌗🌘) and localized
   name (`moon.phase.<0-7>`); its title includes the illuminated
   percentage $\lfloor 100 \cdot (1 - \cos 2\pi f)/2 + 0.5 \rfloor$
   (`moon.lit`).

## Consequences

- The mean synodic month can differ from the true phase instant by up to
  ~14 hours near phase boundaries.
- Emoji depict northern-hemisphere orientation.

## References

- Jean Meeus, *Astronomical Algorithms*, 2nd ed., ch. 49
- ADR 67, ADR 82
- `addons/moon/moon.go`, `addons/moon/web/moon.go`
