// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat
// Seasonal snow, grown and flown rather than drawn.
//
// The crystals come out of Reiter's local cellular model of snow
// crystal growth on a hexagonal lattice (C. A. Reiter, "A local
// cellular model for snow crystal growth", Chaos, Solitons & Fractals
// 23 (2005) 1111-1119): each cell holds a water content, cells that
// are frozen or beside something frozen are "receptive" and take a
// constant gamma of vapour, and the rest diffuses. Beta - how much
// vapour the air starts with - and gamma decide whether the crystal
// comes out a fern, a star or a plate, which is the same thing that
// decides it in the sky. The richer model (Gravner and Griffeath,
// "Modeling snow crystal growth II", Physica D 237 (2008) 385-404)
// takes seven parameters and a much bigger lattice; this one is two
// numbers and looks right.
//
// The fall is what a real flake does: a terminal velocity that grows
// with the flake (Locatelli and Hobbs, "Fall speeds and masses of solid
// precipitation particles", J. Geophys. Res. 79 (1974) 2185-2197), and
// a horizontal flutter that is not a sine. Shed vortices make a falling
// crystal zigzag, so the sideways velocity is an Ornstein-Uhlenbeck
// walk - it pulls towards the wind and wanders around it - and the wind
// itself is interpolated noise rather than a wave. Tumbling follows
// from it: a flake turns fastest when it is sliding sideways.
const SNOW_GRID = 121;
const SNOW_STEPS = 4000;
// How fast the slowest and the fastest flake fall, in pixels a frame
// before depth scales them. At sixty frames a second these cross a
// laptop's screen in something between half a minute and a minute and
// a half, which is a window in falling snow rather than a screensaver.
const SNOW_FALL_SLOW = 0.12;
const SNOW_FALL_FAST = 0.26;
// How far a flake's own air wanders, in pixels a frame: it slants a
// flake's path, it does not fly it across.
const SNOW_WANDER = 0.064;
// The frame the numbers here count in. A step counts the frames of this
// length its time spans, so a 120 Hz screen does not fall twice as fast.
const SNOW_FRAME = 1 / 60;

// One crystal on the hexagonal lattice. Rows are offset, so a cell's
// six neighbours are the two beside it and two on each of the rows
// above and below.
const growCrystal = (alpha, beta, gamma) => {
	const n = SNOW_GRID;
	const mid = (n - 1) >> 1;
	let s = new Float32Array(n * n).fill(beta);
	let next = new Float32Array(n * n);
	const receptive = new Uint8Array(n * n);
	// The six neighbours of every cell, worked out once: allocating a
	// list per cell per step is most of the cost of this model.
	const near = new Int32Array(n * n * 6);
	for (let y = 0; y < n; y++) {
		for (let x = 0; x < n; x++) {
			const odd = y & 1;
			const around = [
				[x - 1, y], [x + 1, y],
				[x - 1 + odd, y - 1], [x + odd, y - 1],
				[x - 1 + odd, y + 1], [x + odd, y + 1],
			];
			for (let k = 0; k < 6; k++) {
				const [nx, ny] = around[k];
				near[(y * n + x) * 6 + k] =
					nx < 0 || ny < 0 || nx >= n || ny >= n ? -1 : ny * n + nx;
			}
		}
	}
	s[mid * n + mid] = 1;
	// Growth stops before the crystal reaches the lattice: past that the
	// boundary feeds it and it comes out the shape of the grid.
	const limit = mid * 0.8;
	let reach = 0;
	for (let step = 0; step < SNOW_STEPS && reach < limit; step++) {
		receptive.fill(0);
		for (let i = 0; i < s.length; i++) {
			if (s[i] < 1) {
				continue;
			}
			receptive[i] = 1;
			for (let k = 0; k < 6; k++) {
				const j = near[i * 6 + k];
				if (j >= 0) {
					receptive[j] = 1;
				}
			}
		}
		for (let i = 0; i < s.length; i++) {
			// The receptive cells keep their water and take the constant
			// addition; the rest is what diffuses. Diffusion runs over
			// every cell, receptive ones included - they hold no
			// diffusing water of their own but they collect what their
			// neighbours give up, and that inflow is why a tip outruns
			// a flat edge and the crystal branches at all.
			const u = receptive[i] ? 0 : s[i];
			let sum = 0;
			for (let k = 0; k < 6; k++) {
				const j = near[i * 6 + k];
				// Off the lattice the air holds its starting vapour, and
				// a receptive neighbour holds none that can diffuse.
				sum += j < 0 ? beta : (receptive[j] ? 0 : s[j]);
			}
			next[i] = (receptive[i] ? s[i] + gamma : 0) + u + (alpha / 12) * (sum - 6 * u);
		}
		const swap = s;
		s = next;
		next = swap;
		if (step % 25 === 0) {
			reach = 0;
			for (let y = 0; y < n; y++) {
				for (let x = 0; x < n; x++) {
					if (s[y * n + x] < 1) {
						continue;
					}
					const dx = x - mid + ((y & 1) ? 0.5 : 0);
					const dy = (y - mid) * 0.87;
					const d = Math.hypot(dx, dy);
					if (d > reach) {
						reach = d;
					}
				}
			}
		}
	}
	return { cells: s, n: n };
};

// The crystal painted into a canvas of its own: a hexagon per frozen
// cell, and the cells that are nearly frozen drawn faintly, which is
// the thin plate a real crystal carries between its arms.
const crystalSprite = (crystal, colour, size) => {
	const n = crystal.n;
	let minX = n, maxX = 0, minY = n, maxY = 0;
	for (let y = 0; y < n; y++) {
		for (let x = 0; x < n; x++) {
			if (crystal.cells[y * n + x] < 1) {
				continue;
			}
			minX = Math.min(minX, x);
			maxX = Math.max(maxX, x);
			minY = Math.min(minY, y);
			maxY = Math.max(maxY, y);
		}
	}
	const sprite = document.createElement("canvas");
	sprite.width = sprite.height = size;
	if (maxX < minX) {
		return sprite;
	}
	const c = sprite.getContext("2d");
	const wide = Math.max(maxX - minX + 1, (maxY - minY + 1) * 0.87);
	const cell = size / (wide + 2);
	const offX = (size - (maxX - minX + 1) * cell) / 2;
	const offY = (size - (maxY - minY + 1) * cell * 0.87) / 2;
	const hexagon = (cx, cy, r) => {
		c.beginPath();
		for (let k = 0; k < 6; k++) {
			const a = (Math.PI / 3) * k + Math.PI / 6;
			const px = cx + r * Math.cos(a);
			const py = cy + r * Math.sin(a);
			if (k === 0) {
				c.moveTo(px, py);
			} else {
				c.lineTo(px, py);
			}
		}
		c.closePath();
		c.fill();
	};
	c.fillStyle = colour;
	for (let y = minY; y <= maxY; y++) {
		for (let x = minX; x <= maxX; x++) {
			const value = crystal.cells[y * n + x];
			if (value < 0.92) {
				continue;
			}
			const odd = y & 1;
			const cx = offX + (x - minX + odd * 0.5 + 0.5) * cell;
			const cy = offY + (y - minY + 0.5) * cell * 0.87;
			// Frozen is solid; the ring just short of freezing is the
			// plate, and it is faint.
			c.globalAlpha = value >= 1 ? 1 : 0.28;
			hexagon(cx, cy, cell * 0.62);
		}
	}
	c.globalAlpha = 1;
	return sprite;
};

export default {
	area: 26000,
	most: Infinity,
	opacity: 0.55,
	sprites: (style) => {
		// Four crystals from Reiter's own range, picked for being four
		// different things rather than four of one: a stellar dendrite,
		// a broader-armed one, a fern, and a sectored plate. Beta is
		// how much vapour the air holds and gamma how fast it is added,
		// which is what decides the habit in the sky too.
		return [
			[1, 0.35, 0.0005],
			[1, 0.45, 0.0008],
			[1, 0.6, 0.0001],
			[1, 0.8, 0.01],
		].map(([a, b, g]) => crystalSprite(growCrystal(a, b, g), style.color, 128));
	},
	make: (view, sprites, noise) => {
		const z = 0.4 + Math.random() * 0.8;
		// Snow is mostly small flakes with a few large ones: the
		// size distribution of aggregate snowflakes is exponential
		// (Gunn and Marshall, "The distribution with size of
		// aggregate snowflakes", J. Meteorol. 15 (1958) 452-461),
		// so the size is drawn from one rather than spread evenly.
		const draw = -Math.log(1 - Math.random() * 0.985) / 1.6;
		return {
			x: Math.random() * view.w,
			y: Math.random() * view.h,
			sprite: Math.floor(Math.random() * sprites.length),
			size: (9 + Math.min(draw, 2.4) * 13) * z,
			z: z,
			// A bigger flake falls faster, and none of them falls at
			// the same speed twice. The window is not three metres
			// of air, it is a window: a third of a pixel a frame
			// crosses it in half a minute, which is what unrimed
			// dendrites drifting past look like.
			fall: (SNOW_FALL_SLOW + Math.random() * (SNOW_FALL_FAST - SNOW_FALL_SLOW)) * (0.55 + z),
			vx: 0,
			vy: 0,
			// How quickly it catches up with the air around it: a
			// small flake follows the wind, a large one lags.
			pull: 0.02 + Math.random() * 0.03,
			// The flutter of a falling plate is not noise: shed
			// vortices rock it from side to side at a rate of its
			// own, an amplitude of about its own width, and a phase
			// nobody else shares (the regimes of a falling disk -
			// steady, fluttering, tumbling - are Field, Klaus, Moore
			// and Nori, Nature 388 (1997) 252-254).
			beat: 0.012 + Math.random() * 0.02,
			beatPhase: Math.random() * Math.PI * 2,
			swing: 0.25 + Math.random() * 0.5,
			// And the air itself is never still: a slow wander of
			// this flake's own, smooth rather than shaken.
			drift: noise((0.004 + Math.random() * 0.004) / SNOW_FRAME),
			// The angle it hangs at, which the rocking works about.
			rest: Math.random() * Math.PI * 2,
			turn: 0,
		};
	},
	step: (f, dt, air, view) => {
		const frames = dt / SNOW_FRAME;
		// Where the air is taking it: the wind, its own slow wander,
		// and the side-to-side beat of the vortices it sheds. The
		// flake lags all three, which is what keeps the path smooth.
		// The same slow wander that pushes it sideways also stretches
		// and squeezes the beat, so the flutter never comes out as a
		// clean sine - shedding wanders in period as well as phase.
		const wander = f.drift(dt);
		f.beatPhase += f.beat * (1 + wander * 0.3) * frames;
		const along = air.x * SNOW_FRAME * f.z
			+ wander * SNOW_WANDER
			+ Math.sin(f.beatPhase) * f.swing * f.fall;
		const catchUp = 1 - (1 - f.pull) ** frames;
		f.vx += (along - f.vx) * catchUp;
		f.vy += (air.y * SNOW_FRAME * f.z - f.vy) * catchUp;
		f.x += f.vx * frames;
		f.y += (f.fall + f.vy) * frames;
		// A fluttering plate rocks with the beat rather than
		// spinning: the turn follows the same phase, a quarter of a
		// cycle behind, and never goes right round.
		f.turn = f.rest + Math.sin(f.beatPhase - Math.PI / 2) * 0.5;
		if (f.y - f.size > view.h) {
			f.y = -f.size;
			f.x = Math.random() * view.w;
			f.vx = 0;
			f.vy = 0;
		}
		if (f.x < -f.size) {
			f.x = view.w + f.size;
		} else if (f.x > view.w + f.size) {
			f.x = -f.size;
		}
	},
	draw: (c, f, sprites) => {
		c.globalAlpha = Math.min(1, 0.3 + f.z * 0.6);
		c.save();
		c.translate(f.x, f.y);
		c.rotate(f.turn);
		c.drawImage(sprites[f.sprite], -f.size / 2, -f.size / 2, f.size, f.size);
		c.restore();
	},
};
// @license-end
