// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat
(() => {
// What the season lets fall on the page of a reader who asked for it
// (ADR 77). The theme names it in --season-weather; each kind is a module
// of its own beside this script - its sprites, how many, how one moves -
// and this owns what they share: the canvas, the air (the theme's wind, the
// phone's tilt, a drag, the puff of a click), the clock, visibility,
// resizing, the message rule and reduced motion.
//
// A kind's module exports:
//   area      square pixels of window per thing
//   most      the most things at once, whatever the window
//   opacity   of the whole canvas
//   sprites(style)                    what it paints, from the canvas's computed style
//   make(view, sprites, noise)        one thing somewhere in the window, at its x and y
//   step(thing, dt, air, view)        moves it dt seconds on in air {x, y}, px/s, y down
//   draw(context, thing, sprites)
const kinds = new URL("season/", document.currentScript.src);
// The longest step a frame takes, in seconds. A longer gap is a stall or a
// tab coming back, not time the air moved, and stepping it whole would
// throw everything across the window at once.
const MAX_STEP = 0.05;
// How fast the gust and the squall wander, in keyframes a second: the
// rates snow was tuned at, a frame at sixty a second.
const GUST_RATE = 0.096;
const SQUALL_RATE = 0.024;
// How far the squall and the gust swing the wind, as shares of the theme's
// mean. Surface wind gusts to about twice its mean and lulls near calm, so
// together they span 0.1 to 1.9 of it and never turn it round.
const SQUALL_SHARE = 0.5;
const GUST_SHARE = 0.4;
// The reader's own air, in pixels a second along the screen. A full tilt
// can stop the default wind or double it and no more: the phone nudges
// the weather, it does not pour it. Full at a wrist's turn, eased over
// half a second so the sensor's tremor does not shake the leaves.
const TILT_MOST = 10;
const TILT_FULL = 30;
const TILT_EASE = 0.5;
// A drag lends a share of its speed as a gust that is gone in about a
// second. A click that wobbles a few pixels is not a drag.
const DRAG_SHARE = 0.3;
const DRAG_MOST = 120;
const DRAG_FADE = 0.4;
const DRAG_SLOP = 10;
// A click or a tap puffs the air outward from where it landed: things
// within the radius take up to PUSH_MOST px/s at its centre, nothing at
// its rim, fading as a drag's gust does. A leaf carried by that air moves
// about PUSH_MOST * PUSH_FADE at its centre, half the radius, and its drag
// answers the change as it answers a gust.
const PUSH_RADIUS = 120;
const PUSH_MOST = 150;
const PUSH_FADE = 0.4;

// Interpolated noise: a walk between random keyframes, smoothed, rate
// keyframes a second. One gust is a wave; weather is not - and a fresh
// random number every frame is not weather either, it is jitter.
const noiseField = (rate) => {
	let from = 0;
	let to = (Math.random() * 2 - 1);
	let at = 0;
	return (dt) => {
		at += rate * dt;
		if (at >= 1) {
			at -= 1;
			from = to;
			to = Math.random() * 2 - 1;
		}
		const smooth = at * at * (3 - 2 * at);
		return from + (to - from) * smooth;
	};
};

// The canvas hangs from the root, outside the body a boosted navigation
// replaces, so it falls on from page to page. Anything falling over
// somebody's mail is in the way, so it stops on a message.
const canvas = document.createElement("canvas");
canvas.className = "season";
canvas.setAttribute("aria-hidden", "true");
// The colour is the theme's faintest text, so a kind that paints in it
// gets grey in a light scheme and pale in a dark one.
canvas.style.cssText = "position: fixed; inset: 0; width: 100%; height: 100%; pointer-events: none; z-index: 1; color: var(--fg-faint)";
const reading = () => {
	canvas.hidden = document.querySelector("main.message") !== null;
};
// Reduced motion is answered by drawing nothing, not by freezing a field of dots.
const fall = async () => {
	if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
		return;
	}
	const name = getComputedStyle(document.documentElement).getPropertyValue("--season-weather").trim();
	if (name === "none") {
		return;
	}
	const kind = (await import(new URL(name + ".js", kinds))).default;
	// Pixels a second towards the inline end: the scene mirrors in a
	// right-to-left page, and the smoke and the leaves keep to one wind.
	const mean = Number(getComputedStyle(document.documentElement).getPropertyValue("--season-wind"));
	if (Number.isNaN(mean)) {
		throw new TypeError("--season-wind is not a number");
	}
	canvas.style.opacity = kind.opacity;
	document.documentElement.appendChild(canvas);
	reading();
	document.addEventListener("htmx:afterSwap", reading);
	const ctx2d = canvas.getContext("2d");
	const view = { w: 0, h: 0 };
	let things = [];
	// The click's air around each thing, by the same index.
	let puffs = [];
	const air = { x: 0, y: 0 };
	let sprites = [];
	let running = false;
	let last = 0;
	const gust = noiseField(GUST_RATE);
	const squall = noiseField(SQUALL_RATE);
	let tilted = 0;
	let tilt = 0;
	let drag = 0;
	let press = null;

	const resize = () => {
		const dpr = window.devicePixelRatio || 1;
		view.w = window.innerWidth;
		view.h = window.innerHeight;
		canvas.width = Math.floor(view.w * dpr);
		canvas.height = Math.floor(view.h * dpr);
		ctx2d.setTransform(dpr, 0, 0, dpr, 0, 0);
		const wanted = Math.min(kind.most, Math.round((view.w * view.h) / kind.area));
		things = [];
		puffs = [];
		for (let i = 0; i < wanted; i++) {
			things.push(kind.make(view, sprites, noiseField));
			puffs.push({ x: 0, y: 0 });
		}
	};

	const frame = (now) => {
		if (!running) {
			return;
		}
		const dt = last ? Math.min((now - last) / 1000, MAX_STEP) : 0;
		last = now;
		// The wind is a slow squall with a gust on top of it.
		const toward = document.documentElement.dir === "rtl" ? -1 : 1;
		tilt += (tilted - tilt) * (1 - Math.exp(-dt / TILT_EASE));
		drag *= Math.exp(-dt / DRAG_FADE);
		const wind = toward * mean * (1 + squall(dt) * SQUALL_SHARE + gust(dt) * GUST_SHARE) + tilt + drag;
		const fade = Math.exp(-dt / PUSH_FADE);
		ctx2d.clearRect(0, 0, view.w, view.h);
		for (let i = 0; i < things.length; i++) {
			const p = puffs[i];
			p.x *= fade;
			p.y *= fade;
			air.x = wind + p.x;
			air.y = p.y;
			kind.step(things[i], dt, air, view);
			kind.draw(ctx2d, things[i], sprites);
		}
		ctx2d.globalAlpha = 1;
		requestAnimationFrame(frame);
	};
	const start = () => {
		if (running) {
			return;
		}
		running = true;
		last = 0;
		requestAnimationFrame(frame);
	};
	// A hidden tab is a tab drawing nothing: the browser throttles the
	// frames anyway, and a laptop should not spend a battery on a page
	// nobody is looking at.
	const onVisible = () => {
		running = false;
		drag = 0;
		press = null;
		for (const p of puffs) {
			p.x = 0;
			p.y = 0;
		}
		if (!document.hidden) {
			start();
		}
	};
	document.addEventListener("visibilitychange", onVisible);
	// Tilt towards the screen's right, however the phone is turned.
	window.addEventListener("deviceorientation", (e) => {
		if (document.hidden || e.gamma === null) {
			return;
		}
		const side = { 0: e.gamma, 90: e.beta, 180: -e.gamma, 270: -e.beta }[screen.orientation.angle];
		tilted = TILT_MOST * Math.max(-1, Math.min(1, side / TILT_FULL));
	});
	// iOS lets a page feel the tilt only once a tap has asked. The reader
	// answered when ticking the season, so this asks again without a prompt.
	if (window.DeviceOrientationEvent && typeof DeviceOrientationEvent.requestPermission === "function") {
		window.addEventListener("pointerup", () => DeviceOrientationEvent.requestPermission(), { once: true, passive: true });
	}
	// Only listening: a drag still selects, scrolls and swipes as before,
	// and one the browser takes for a scroll is cancelled and blows nothing.
	window.addEventListener("pointerdown", (e) => {
		press = { x: e.clientX, y: e.clientY, at: e.timeStamp, far: false };
	}, { passive: true });
	window.addEventListener("pointermove", (e) => {
		if (press === null || document.hidden) {
			return;
		}
		if (!press.far && Math.hypot(e.clientX - press.x, e.clientY - press.y) < DRAG_SLOP) {
			return;
		}
		press.far = true;
		const dt = (e.timeStamp - press.at) / 1000;
		if (dt > 0) {
			drag = Math.max(-DRAG_MOST, Math.min(DRAG_MOST, (e.clientX - press.x) / dt * DRAG_SHARE));
		}
		press.x = e.clientX;
		press.y = e.clientY;
		press.at = e.timeStamp;
	}, { passive: true });
	const puff = (x, y) => {
		for (let i = 0; i < things.length; i++) {
			const dx = things[i].x - x;
			const dy = things[i].y - y;
			const d = Math.hypot(dx, dy);
			if (d < PUSH_RADIUS && d > 0) {
				const s = PUSH_MOST * (1 - d / PUSH_RADIUS) / d;
				puffs[i].x += dx * s;
				puffs[i].y += dy * s;
			}
		}
	};
	// A press that never moved past the slop is a click: it puffs and
	// still clicks, since nothing here prevents the default.
	window.addEventListener("pointerup", (e) => {
		if (press !== null && !press.far && !document.hidden) {
			puff(e.clientX, e.clientY);
		}
		press = null;
	}, { passive: true });
	window.addEventListener("pointercancel", () => {
		press = null;
	}, { passive: true });
	window.addEventListener("resize", resize);
	// Painting the sprites can be expensive, so it waits for the browser
	// to be idle rather than delaying the first paint.
	const prepare = () => {
		sprites = kind.sprites(getComputedStyle(canvas));
		resize();
		start();
	};
	if (window.requestIdleCallback) {
		window.requestIdleCallback(prepare, { timeout: 2000 });
	} else {
		setTimeout(prepare, 200);
	}
};
fall();
})();
// @license-end
