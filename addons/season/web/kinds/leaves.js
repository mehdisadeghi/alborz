// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat
// Autumn leaves, falling the way a plate falls in air.
//
// A leaf is a thin plate at a Reynolds number near a thousand, and
// Andersen, Pesavento and Wang give the reduced model for exactly that
// ("Analysis of transitions between fluttering, tumbling and steady
// descent of falling cards", J. Fluid Mech. 541 (2005) 91-104): the
// plate's velocity (vx along it, vy across it) and angle in its own
// frame, a circulation that gives lift, a drag that depends on the angle
// of attack, and a damped torque, eqs. (5.1)-(5.3) with (4.7)-(4.9).
// The dimensionless moment of inertia I* alone decides whether a leaf
// rocks from side to side, tumbles end over end, or does both by turns.
// The paper's scales (4.1)-(4.2) make it real: lengths in half-chords a,
// speeds in U = sqrt(I* g a) for a leaf far denser than air, so a leaf
// rocks and falls in real time, seen from LEAF_VIEW.
//
// The outlines are four trees of the Hyrcanian forest on the Alborz's
// northern slope, proportions from the floras: Persian ironwood
// (Parrotia persica), velvet maple (Acer velutinum), oriental beech
// (Fagus orientalis) and chestnut-leaved oak (Quercus castaneifolia).

// The paper's coefficients, which it fits to its own Navier-Stokes runs.
const LEAF_CT = 1.2;
const LEAF_CR = Math.PI;
const LEAF_A = 1.4;
const LEAF_B = 1.0;
const LEAF_MU1 = 0.2;
const LEAF_MU2 = 0.2;
const LEAF_G = 9.81;
// The half-chord in metres: half an 8 cm blade, mid-range for the four
// trees. With I* it fixes the leaf's mass per area, 2 I* rho_air a.
const LEAF_HALF_CHORD = 0.04;
// CSS pixels per metre of the air the leaves fall through: a window some
// fifteen metres tall, the leaves of a tree across the valley. The model
// gives a median fall of 0.25 m/s, so they drift down at 13 px/s while
// rocking at the real 0.6-1 s; the sprites are drawn larger than that
// distance makes them, or nobody would see them.
const LEAF_VIEW = 53;
// The longest RK4 step in model time: the turning points of a flutter
// last about one unit, and a frame is up to four.
const LEAF_STEP = 0.1;
// Time units integrated before a leaf is first seen, so none is seen
// being let go: the regimes settle within a few periods.
const LEAF_SETTLE = 40;
// How hard the wind pushes, in pixels a second at the strongest gust.
const LEAF_WIND = 14;
// The smallest and largest leaf, as the sprite's length in pixels.
const LEAF_SMALL = 18;
const LEAF_LARGE = 32;
const LEAF_SPRITE = 64;
// The sprite's side in half-chords: the outline's length of two and a margin.
const LEAF_BOX = 2.2;

// I* by regime: the paper's flutter (1.1), tumble (1.4, 1.45) and chaos
// (1.8-2.8), in shares that keep the page mostly rocking. The band
// between flutter and tumble is left out: the period diverges there.
const LEAF_REGIMES = [
	{ share: 0.7, low: 0.6, high: 1.1 },
	{ share: 0.2, low: 1.3, high: 1.5 },
	{ share: 0.1, low: 1.8, high: 2.6 },
];

const inertia = () => {
	let pick = Math.random();
	for (const r of LEAF_REGIMES) {
		if (pick < r.share) {
			return r.low + Math.random() * (r.high - r.low);
		}
		pick -= r.share;
	}
	return LEAF_REGIMES[0].high;
};

// The derivative of the state [vx, vy, omega, theta, x, y], y up.
const slope = (s, I) => {
	const [vx, vy, w, th] = s;
	const q = Math.hypot(vx, vy) || 1e-9;
	const circulation = (2 / Math.PI) * (-LEAF_CT * vx * vy / q + LEAF_CR * w);
	const drag = (1 / Math.PI) * (LEAF_A - LEAF_B * (vx * vx - vy * vy) / (q * q)) * q;
	const c = Math.cos(th);
	const n = Math.sin(th);
	return [
		((I + 1) * w * vy - circulation * vy - n - drag * vx) / I,
		(-I * w * vx + circulation * vx - c - drag * vy) / (I + 1),
		(-vx * vy - (LEAF_MU1 + LEAF_MU2 * Math.abs(w)) * w) / (0.25 * (I + 0.5)),
		w,
		vx * c - vy * n,
		vx * n + vy * c,
	];
};

// One fourth-order Runge-Kutta step of h time units.
const advance = (s, I, h) => {
	const at = (k, f) => s.map((v, i) => v + k[i] * f);
	const k1 = slope(s, I);
	const k2 = slope(at(k1, h / 2), I);
	const k3 = slope(at(k2, h / 2), I);
	const k4 = slope(at(k3, h), I);
	return s.map((v, i) => v + (h / 6) * (k1[i] + 2 * k2[i] + 2 * k3[i] + k4[i]));
};

// Each outline runs along the midrib from the stalk (-1) to the tip (1),
// so the plate's chord is the sprite's x axis. A profile gives the
// half-width at s from the stalk (0) to the tip (1), in leaf lengths;
// side is 1 on one half and -1 on the other.
const LEAF_STEPS = 120;
const bilateral = (profile) => {
	const p = new Path2D();
	for (let i = 0; i <= LEAF_STEPS; i++) {
		const s = i / LEAF_STEPS;
		p.lineTo(-1 + 2 * s, 2 * profile(s, 1));
	}
	for (let i = LEAF_STEPS; i >= 0; i--) {
		const s = i / LEAF_STEPS;
		p.lineTo(-1 + 2 * s, -2 * profile(s, -1));
	}
	p.closePath();
	return p;
};

const outlines = {
	// Obovate, about half as wide as long, a wedge at the base, shallow
	// teeth above the middle, one half a little wider than the other.
	ironwood: () => bilateral((s, side) => 0.27 * Math.sin(Math.PI * s ** 1.35) ** 0.6 * (1 + 0.05 * side)
		+ (s > 0.5 ? 0.02 * Math.abs(Math.sin(10 * Math.PI * s)) * (s - 0.5) * 2 : 0)),
	// Elliptic to obovate, 0.6 as wide as long, a vein pair to every
	// wave of a margin that is barely wavy.
	beech: () => bilateral((s) => 0.29 * Math.sin(Math.PI * s ** 1.15) ** 0.75 * (1 + 0.035 * Math.sin(20 * Math.PI * s))),
	// Narrowly elliptic, 0.38 as wide as long, eleven sharp teeth that
	// point forward, as each vein runs out into one.
	oak: () => bilateral((s) => 0.15 * Math.sin(Math.PI * s ** 0.95) ** 0.8
		+ (s > 0.06 && s < 0.96 ? 0.045 * ((s * 11) % 1) ** 1.6 * Math.sin(Math.PI * s) : 0)),
	// Five broad lobes with pointed tips, the lower two smaller, the
	// sinuses about halfway in, a heart-shaped base and coarse teeth.
	// Drawn round its centre rather than along a profile.
	maple: () => {
		const lobes = [[0, 1], [60, 0.92], [-60, 0.92], [125, 0.62], [-125, 0.62]];
		const p = new Path2D();
		for (let i = 0; i < 720; i++) {
			const deg = i / 2 - 180;
			let r = 0.46;
			for (const [at, reach] of lobes) {
				const d = Math.abs(((deg - at + 540) % 360) - 180);
				if (d < 34) {
					const lobe = Math.cos((d / 34) * Math.PI / 2) ** 0.55 * 0.78 + 0.22 * Math.max(0, 1 - d / 11) ** 1.5;
					r = Math.max(r, 0.46 + (reach - 0.46) * lobe);
				}
			}
			if (r > 0.6) {
				r *= 1 + 0.035 * Math.abs(Math.sin((deg * Math.PI / 180) * 26));
			}
			if (Math.abs(deg) > 168) {
				r = Math.min(r, 0.28);
			}
			const a = deg * Math.PI / 180;
			p.lineTo(r * Math.cos(a) - 0.1, r * Math.sin(a));
		}
		p.closePath();
		return p;
	},
};

// The pigments each tree turns, by token name.
const LEAF_PIGMENTS = {
	ironwood: ["anthocyanin", "anthocyanin-deep", "carotenoid"],
	maple: ["carotenoid", "chlorophyll"],
	beech: ["carotenoid-deep", "tannin"],
	oak: ["tannin", "carotenoid"],
};

const leafSprite = (outline, colour) => {
	const sprite = document.createElement("canvas");
	sprite.width = sprite.height = LEAF_SPRITE;
	const c = sprite.getContext("2d");
	const unit = LEAF_SPRITE / LEAF_BOX;
	c.translate(LEAF_SPRITE / 2, LEAF_SPRITE / 2);
	c.scale(unit, unit);
	c.fillStyle = colour;
	c.fill(outline);
	// The midrib, darker than the blade.
	c.strokeStyle = "rgba(0, 0, 0, 0.3)";
	c.lineWidth = 1.2 / unit;
	c.beginPath();
	c.moveTo(-1.05, 0);
	c.lineTo(0.85, 0);
	c.stroke();
	return sprite;
};

export default {
	area: 60000,
	most: 40,
	opacity: 0.9,
	sprites: (style) => Object.entries(LEAF_PIGMENTS).flatMap(([tree, pigments]) => {
		const outline = outlines[tree]();
		return pigments.map((p) => leafSprite(outline, style.getPropertyValue("--season-leaf-" + p).trim()));
	}),
	make: (view, sprites) => {
		const I = inertia();
		// A small tilt either way starts the motion; the sign picks the
		// way a tumbling leaf glides.
		let state = [0, 0, 0, (Math.random() - 0.5) * 0.6, 0, 0];
		for (let t = 0; t < LEAF_SETTLE; t += 0.1) {
			state = advance(state, I, 0.1);
		}
		const length = LEAF_SMALL + Math.random() * (LEAF_LARGE - LEAF_SMALL);
		// Pixels per half-chord and seconds per unit of the model's time.
		const speed = Math.sqrt(I * LEAF_G * LEAF_HALF_CHORD);
		return {
			I: I,
			unit: LEAF_HALF_CHORD * LEAF_VIEW,
			time: LEAF_HALF_CHORD / speed,
			state: state,
			length: length,
			sprite: Math.floor(Math.random() * sprites.length),
			x: Math.random() * view.w,
			y: Math.random() * view.h,
		};
	},
	step: (leaf, dt, wind, view) => {
		const a = leaf.unit;
		const before = leaf.state;
		const steps = Math.ceil(dt / leaf.time / LEAF_STEP);
		for (let i = 0; i < steps; i++) {
			leaf.state = advance(leaf.state, leaf.I, dt / leaf.time / steps);
		}
		// Wind adds to the lab position: exact for a steady wind, near
		// enough for a gust. The model's y points up, the screen's down.
		leaf.x += (leaf.state[4] - before[4]) * a + wind * LEAF_WIND * dt;
		leaf.y -= (leaf.state[5] - before[5]) * a;
		const size = leaf.length;
		if (leaf.y - size > view.h) {
			leaf.y = -size;
			leaf.x = Math.random() * view.w;
		}
		if (leaf.x < -size) {
			leaf.x = view.w + size;
		} else if (leaf.x > view.w + size) {
			leaf.x = -size;
		}
	},
	draw: (c, leaf, sprites) => {
		const drawn = leaf.length * LEAF_BOX / 2;
		c.save();
		c.translate(leaf.x, leaf.y);
		// The model's angle is anticlockwise with y up; the screen turns clockwise.
		c.rotate(-leaf.state[3]);
		c.drawImage(sprites[leaf.sprite], -drawn / 2, -drawn / 2, drawn, drawn);
		c.restore();
	},
};
// @license-end
