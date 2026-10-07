// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat
// Summer light: shafts from a sun above the top corner at the inline end,
// where the scene's house stands, slowly brightening and fading as thin
// cloud and haze pass, and swaying a little.
//
// Nothing falls, so the air does not move them. A shaft is one soft wedge
// painted once and turned into place each frame.

// How many shafts, and the angles between which they fan out from the
// corner, in degrees from the top edge towards the inline start.
const RAY_COUNT = 5;
const RAY_FROM = 18;
const RAY_TO = 72;
// The most a shaft sways either way, in degrees, and the fastest and
// slowest it breathes, in cycles a second: slow enough to be noticed
// only when looked for.
const RAY_SWAY = 2.5;
const RAY_FAST = 1 / 18;
const RAY_SLOW = 1 / 40;
// The brightest a shaft gets at its root; it fades to nothing along it.
const RAY_ALPHA = 0.22;
// The sprite: a wedge along x, its root at the left edge.
const RAY_LENGTH = 512;
const RAY_WIDTH = 128;

const raySprite = (colour) => {
	const sprite = document.createElement("canvas");
	sprite.width = RAY_LENGTH;
	sprite.height = RAY_WIDTH;
	const c = sprite.getContext("2d");
	const along = c.createLinearGradient(0, 0, RAY_LENGTH, 0);
	along.addColorStop(0, colour);
	along.addColorStop(1, "transparent");
	c.fillStyle = along;
	c.beginPath();
	c.moveTo(0, RAY_WIDTH / 2 - 4);
	c.lineTo(RAY_LENGTH, 0);
	c.lineTo(RAY_LENGTH, RAY_WIDTH);
	c.lineTo(0, RAY_WIDTH / 2 + 4);
	c.closePath();
	// The edges blur into the air rather than cut it.
	c.filter = "blur(10px)";
	c.fill();
	return sprite;
};

export default {
	// One shaft per pixel, so the count is RAY_COUNT on any window.
	area: 1,
	most: RAY_COUNT,
	opacity: 1,
	// A shaft is a blur, so a sixty-fourth of the pixels draws the same
	// light at a fraction of the cost where the canvas is not on a GPU.
	detail: 0.125,
	sprites: (style) => [raySprite(style.getPropertyValue("--season-sunray").trim())],
	make: () => ({
		x: 0,
		y: 0,
		toward: 1,
		length: 0,
		angle: RAY_FROM + Math.random() * (RAY_TO - RAY_FROM),
		width: 0.6 + Math.random() * 0.8,
		rate: RAY_SLOW + Math.random() * (RAY_FAST - RAY_SLOW),
		phase: Math.random() * Math.PI * 2,
	}),
	step: (ray, dt, air, view) => {
		ray.phase += 2 * Math.PI * ray.rate * dt;
		ray.toward = view.toward;
		ray.x = view.toward > 0 ? view.w : 0;
		ray.length = Math.hypot(view.w, view.h) * 1.1;
	},
	draw: (c, ray, sprites) => {
		const deg = ray.angle + RAY_SWAY * Math.sin(ray.phase * 0.7);
		// From the top edge towards the inline start, mirrored with the page.
		const turn = (ray.toward > 0 ? 180 - deg : deg) * Math.PI / 180;
		c.save();
		c.globalAlpha = RAY_ALPHA * (0.55 + 0.45 * Math.sin(ray.phase));
		c.translate(ray.x, ray.y);
		c.rotate(turn);
		c.drawImage(sprites[0], 0, -RAY_WIDTH * ray.width * 2, ray.length, RAY_WIDTH * ray.width * 4);
		c.restore();
	},
};
// @license-end
