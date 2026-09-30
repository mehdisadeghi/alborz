const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

// Exercise the complete anchor-positioning fallback used by enhance().
const helpers = fs.readFileSync(path.join(__dirname, '../templates/assets/helpers.js'), 'utf8');
const start = helpers.indexOf('// Without anchor positioning');
assert.notEqual(start, -1, 'the popover fallback exists');
const fallback = helpers.slice(start, helpers.indexOf('\n\n};', start));

function fixture({dir = 'ltr', width = 320, height = 640, panelWidth = 250, panelHeight = 180,
	box = {left: 260, right: 312, top: 580, bottom: 632}, viewport, anchors = false} = {}) {
	const listeners = new Map();
	const panel = {
		id: 'phone-sections', style: {},
		addEventListener(name, fn) { listeners.set(name, [...(listeners.get(name) || []), fn]); },
		getBoundingClientRect() {
			return {width: Math.min(panelWidth, parseFloat(this.style.maxWidth) || Infinity),
				height: Math.min(panelHeight, parseFloat(this.style.maxHeight) || Infinity)};
		},
	};
	const button = {getBoundingClientRect: () => box};
	const document = {documentElement: {dir}, querySelectorAll: () => [panel], querySelector: () => button};
	const window = {innerWidth: width, innerHeight: height, visualViewport: viewport};
	vm.runInNewContext(fallback, {document, window,
		CSS: {supports: () => anchors}, once: () => true});
	const toggle = (newState = 'open') => { for (const fn of listeners.get('toggle') || []) fn({newState}); };
	const rect = () => ({left: parseFloat(panel.style.left), top: parseFloat(panel.style.top), ...panel.getBoundingClientRect()});
	return {panel, toggle, rect, window};
}

test('bottom More opens above its button within the viewport in LTR and RTL', () => {
	for (const dir of ['ltr', 'rtl']) {
		const f = fixture({dir});
		f.toggle();
		const rect = f.rect();
		assert.ok(rect.top + rect.height <= 580, dir + ' opens above');
		assert.ok(rect.left >= 8 && rect.left + rect.width <= 312, dir + ' stays on screen');
	}
});

test('short viewports bound tall and wide menus and keep them scrollable', () => {
	const f = fixture({width: 280, height: 180, panelWidth: 400, panelHeight: 500,
		box: {left: 235, right: 275, top: 130, bottom: 174}});
	f.toggle();
	const rect = f.rect();
	assert.equal(rect.left, 8);
	assert.equal(rect.top, 8);
	assert.equal(rect.width, 264);
	assert.equal(rect.height, 164);
	assert.equal(f.panel.style.overflowY, 'auto');
});

test('existing top toolbar menus keep their downward and directional alignment', () => {
	const box = {left: 100, right: 160, top: 10, bottom: 50};
	for (const [dir, left] of [['ltr', 60], ['rtl', 100]]) {
		const f = fixture({dir, width: 600, box, panelWidth: 100});
		f.toggle();
		assert.equal(f.rect().top, 52);
		assert.equal(f.rect().left, left);
	}
});

test('popover bounds follow an offset visual viewport', () => {
	const f = fixture({viewport: {offsetLeft: 20, offsetTop: 100, width: 280, height: 300},
		box: {left: 270, right: 310, top: 360, bottom: 390}});
	f.toggle();
	const rect = f.rect();
	assert.ok(rect.left >= 28 && rect.left + rect.width <= 292);
	assert.ok(rect.top >= 108 && rect.top + rect.height <= 392);
});

test('reopening after a viewport growth clears the previous size caps', () => {
	const f = fixture({width: 280, height: 180, panelWidth: 400, panelHeight: 500});
	f.toggle();
	assert.equal(f.rect().height, 164);
	f.toggle('closed');
	f.window.innerWidth = 800;
	f.window.innerHeight = 900;
	f.toggle();
	assert.equal(f.rect().width, 400);
	assert.equal(f.rect().height, 500);
});

test('closing and native anchor support leave positioning to the browser', () => {
	const f = fixture();
	f.toggle('closed');
	assert.deepEqual(f.panel.style, {});
	const native = fixture({anchors: true});
	native.toggle();
	assert.deepEqual(native.panel.style, {});
});
