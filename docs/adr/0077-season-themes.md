# ADR 77: Season themes and falling things

Status: accepted, amended by ADR 82 (packaged under `addons/season`) and
ADR 86 (mail read-state contrast). Amends ADR 67.

## Context

Seasonal themes (winter, autumn, spring, summer) share a valley and
house scene at the bottom of the viewport and optional falling weather
particles (snow, leaves, rain, sun rays). Duplicating SVG geometry per
theme or running separate animation loops per particle type would
multiply markup and script maintenance.

## Decision

### Scene

1. The scene consists of independent SVG layers ordered back to front:
   `far`, `conifers` or `broadleaves`, `blossoms`, `near`, `house`,
   `smoke`, `snowcover`, `foreground`. Each layer is a partial
   `scene-<layer>` in `addons/season/web/partials/scene.html` whose
   fills reference `--scene-<layer>[-part]` CSS tokens. The `scene`
   partial stacks them inside `<svg class="scene" id="scene"
   hx-preserve aria-hidden="true" viewBox="0 0 1600 420">`.
2. A theme declares its layers via `--scene` in its stylesheet:
   - winter: `far conifers near house smoke snowcover foreground` (with
     non-repeating night stars inspired by Tycho-2 density);
   - autumn: `far broadleaves near house smoke foreground`;
   - spring: `far broadleaves blossoms near house smoke foreground`;
   - summer: `far broadleaves near house foreground`.
   The server renders the `scene` partial only when `--scene` is
   present; an unknown layer name stops startup.
3. Each seasonal theme defines every `--scene-*` token its layers use in
   all of its scheme blocks. The base stylesheet holds only shared
   `.scene` layout (fixed bottom, `z-index: -1`, `pointer-events: none`,
   RTL horizontal mirroring, smoke keyframes and print hiding).
4. Chimney smoke animates continuously and stands static at opacity 0.35
   under `prefers-reduced-motion: reduce`.

### Falling things

5. Falling weather is an opt-in page add-on (`addons/season`, label
   `season.addon`), off by default and suppressed on message pages and
   under `prefers-reduced-motion: reduce`.
6. The active effect is selected by `--season-weather`: `snow` in the
   base stylesheet, `leaves` in autumn, `rain` in spring, `sunrays` in
   summer, or `none`.
7. One engine (`season.js`) manages the `<html>` canvas, wind, frame
   clock (clamped to 50 ms), resize and visibility, and dynamically
   imports `/addons/season/<kind>.js`.
8. Shared physical scale is `METRE = 53` CSS px/m:
   - **Snow** (`kinds/snow.js`): Reiter crystal sprites, Locatelli-Hobbs
     terminal speeds, Gunn-Marshall sizes, 1 flake per 26,000 px².
   - **Leaves** (`kinds/leaves.js`): integrates the quasi-steady plate
     ODE of Andersen, Pesavento and Wang (JFM 541, 91-104) with RK4 at
     half-chord `a = 4 cm` across fluttering ($I^* \in [0.6, 1.1]$,
     70%), tumbling ($[1.3, 1.5]$, 20%) and chaotic ($[1.8, 2.6]$, 10%)
     regimes; 4 Hyrcanian forest leaf outlines (*Parrotia persica*,
     *Acer velutinum*, *Fagus orientalis*, *Quercus castaneifolia*)
     coloured by pigment tokens (`--season-leaf-*`), opacity 0.85, 1 per
     30,000 px² (max 40).
   - **Rain** (`kinds/rain.js`): drop diameters from Marshall-Palmer at
     5 mm/h ($0.5\text{--}3\text{ mm}$) falling at Atlas-Srivastava-Sekhon
     terminal speeds ($2.0\text{--}8.0\text{ m/s}$), stroked as 0.04 s
     motion streaks 2 px wide in `--season-rain`, 1 per 2,000 px² (max
     300).
   - **Sun rays** (`kinds/sunrays.js`): five soft wedges from the
     inline-end top corner in `--season-sunray`, alpha 0.6, rendered at
     half resolution (`detail: 0.5`).

### Wind

9. Mean wind is `--season-wind` (CSS px/s toward the inline end,
   default 10, autumn 5, spring 40, mirrored in RTL), modulated between
   0.1 and 1.9 of the mean by slow squall and gust noise.
10. Passive user interaction adds to the air velocity without blocking
    scrolling, selection or clicks:
    - device tilt on coarse-pointer devices (`deviceorientation`, up to
      10 px/s at 30°);
    - pointer drag past 10 px (0.3 of drag speed, up to 120 px/s, 0.4 s
      decay);
    - click or tap outward puff within 120 px radius (up to 150 px/s,
      0.4 s decay).

## Consequences

- Scene geometry is defined once and shared across all four seasonal
  themes.
- Theme selection alone never starts animation; the `season` add-on
  must be enabled explicitly and respects `prefers-reduced-motion`.
- A test verifies that every theme declaring `--scene` defines all
  required `--scene-*` tokens across its scheme blocks.

## References

- Andersen, Pesavento, Wang, *J. Fluid Mech.* 541 (2005) 65-90, 91-104
- Marshall, Palmer, *J. Meteorol.* 5 (1948) 165-166; Atlas, Srivastava,
  Sekhon, *Rev. Geophys.* 11 (1973) 1-35
- Høg et al., *A&A* 355 (2000) L27-L30 (Tycho-2)
- ADR 67, ADR 82, ADR 86
- `addons/season/`, `web/prefs.go`, `templates/assets/style.css`
