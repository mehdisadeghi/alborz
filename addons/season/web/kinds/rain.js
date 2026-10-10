// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat
// Spring rain: drops sized and timed as rain is, drawn as the streaks
// the eye sees.
//
// Sizes follow Marshall and Palmer's exponential distribution ("The
// distribution of raindrops with size", J. Meteorol. 5 (1948) 165-166),
// N(D) ~ exp(-Lambda D) with Lambda = 4.1 R^-0.21 per mm at R mm/h. A
// drop falls at its terminal speed, which Atlas, Srivastava and Sekhon
// fit to Gunn and Kinzer's measurements ("Doppler radar characteristics
// of precipitation at vertical incidence", Rev. Geophys. 11 (1973)
// 1-35): v = 9.65 - 10.3 exp(-0.6 D), m/s with D in mm.

// Moderate rain, in mm an hour.
const RAIN_RATE = 5;
// Drops under half a millimetre are drizzle, too small to see as a
// streak; over three they are rare and break up.
const RAIN_SMALLEST = 0.5;
const RAIN_LARGEST = 3;
const RAIN_G = 9.81;
// The exposure of a streak, in seconds: a drop is seen as the line it
// draws in about a twenty-fifth of a second, as in a photograph.
const RAIN_BLUR = 0.04;

const lambda = 4.1 * RAIN_RATE ** -0.21;
// A diameter from the exponential, cut to the drops one sees.
const diameter = () => {
	const low = Math.exp(-lambda * RAIN_SMALLEST);
	const high = Math.exp(-lambda * RAIN_LARGEST);
	return -Math.log(low - Math.random() * (low - high)) / lambda;
};

export default {
	area: 2000,
	most: 300,
	opacity: 1,
	detail: 1,
	sprites: (style) => [style.getPropertyValue("--season-rain").trim()],
	make: (view) => {
		const fall = (9.65 - 10.3 * Math.exp(-0.6 * diameter())) * view.metre;
		return {
			x: Math.random() * view.w,
			y: Math.random() * view.h,
			fall: fall,
			vx: 0,
			vy: fall,
			// A drop at its terminal speed takes about v/g to follow a
			// change of air.
			lag: fall / view.metre / RAIN_G,
		};
	},
	step: (d, dt, air, view) => {
		const follow = 1 - Math.exp(-dt / d.lag);
		d.vx += (air.x - d.vx) * follow;
		d.vy += (d.fall + air.y - d.vy) * follow;
		d.x += d.vx * dt;
		d.y += d.vy * dt;
		const reach = d.fall * RAIN_BLUR;
		if (d.y - reach > view.h) {
			d.y = -reach;
			d.x = Math.random() * view.w;
		}
		if (d.x < -reach) {
			d.x = view.w + reach;
		} else if (d.x > view.w + reach) {
			d.x = -reach;
		}
	},
	draw: (c, d, sprites) => {
		c.strokeStyle = sprites[0];
		c.lineWidth = 2;
		c.beginPath();
		c.moveTo(d.x, d.y);
		c.lineTo(d.x - d.vx * RAIN_BLUR, d.y - d.vy * RAIN_BLUR);
		c.stroke();
	},
};
// @license-end
