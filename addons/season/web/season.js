// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat
(() => {
// What the season lets fall on the page of a reader who asked for it
// (ADR 77). The theme names it in --season-weather; each kind is a module
// of its own beside this script - its sprites, how many, how one moves -
// and this owns what they share: the canvas, the wind, the clock,
// visibility, resizing, the message rule and reduced motion.
//
// A kind's module exports:
//   area      square pixels of window per thing
//   most      the most things at once, whatever the window
//   opacity   of the whole canvas
//   sprites(style)                    what it paints, from the canvas's computed style
//   make(view, sprites, noise)        one thing somewhere in the window
//   step(thing, dt, wind, view)       moves it dt seconds on
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
	canvas.style.opacity = kind.opacity;
	document.documentElement.appendChild(canvas);
	reading();
	document.addEventListener("htmx:afterSwap", reading);
	const ctx2d = canvas.getContext("2d");
	const view = { w: 0, h: 0 };
	let things = [];
	let sprites = [];
	let running = false;
	let last = 0;
	const gust = noiseField(GUST_RATE);
	const squall = noiseField(SQUALL_RATE);

	const resize = () => {
		const dpr = window.devicePixelRatio || 1;
		view.w = window.innerWidth;
		view.h = window.innerHeight;
		canvas.width = Math.floor(view.w * dpr);
		canvas.height = Math.floor(view.h * dpr);
		ctx2d.setTransform(dpr, 0, 0, dpr, 0, 0);
		const wanted = Math.min(kind.most, Math.round((view.w * view.h) / kind.area));
		things = [];
		for (let i = 0; i < wanted; i++) {
			things.push(kind.make(view, sprites, noiseField));
		}
	};

	const frame = (now) => {
		if (!running) {
			return;
		}
		const dt = last ? Math.min((now - last) / 1000, MAX_STEP) : 0;
		last = now;
		// The wind is a slow squall with a gust on top of it.
		const wind = squall(dt) * 1.4 + gust(dt) * 0.6;
		ctx2d.clearRect(0, 0, view.w, view.h);
		for (const t of things) {
			kind.step(t, dt, wind, view);
			kind.draw(ctx2d, t, sprites);
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
		if (!document.hidden) {
			start();
		}
	};
	document.addEventListener("visibilitychange", onVisible);
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
