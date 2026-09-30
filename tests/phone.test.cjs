const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

function fixture({phone = 'bar', width = 390, height = 800, visualViewport = true} = {}) {
	const target = () => {
		const listeners = new Map();
		return {
			addEventListener(name, fn) { listeners.set(name, [...(listeners.get(name) || []), fn]); },
			emit(name) { for (const fn of listeners.get(name) || []) fn(); },
		};
	};
	const root = {dataset: {phone}, clientWidth: width, clientHeight: height,
		style: {setProperty(name, value) { this[name] = value; }, removeProperty(name) { delete this[name]; }}};
	const document = {...target(), documentElement: root, activeElement: null};
	const viewport = visualViewport ? {...target(), width, height, scale: 1} : null;
	const window = {...target(), innerWidth: width, innerHeight: height, visualViewport: viewport};
	const frames = [];
	vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../templates/assets/phone.js'), 'utf8'), {
		document, window, requestAnimationFrame: fn => frames.push(fn),
	});
	const flush = () => { while (frames.length) frames.shift()(); };
	const focus = (tagName = 'INPUT', extra = {}) => {
		document.activeElement = {tagName, type: 'text', ...extra};
		document.emit('focusin');
		flush();
	};
	const resize = ({height: visualHeight, layoutHeight, width: nextWidth, viewportWidth, scale} = {}) => {
		if (visualHeight !== undefined && viewport) viewport.height = visualHeight;
		if (layoutHeight !== undefined) window.innerHeight = root.clientHeight = layoutHeight;
		if (nextWidth !== undefined) {
			window.innerWidth = root.clientWidth = nextWidth;
			if (viewport) viewport.width = nextWidth;
		}
		if (viewportWidth !== undefined && viewport) viewport.width = viewportWidth;
		if (scale !== undefined && viewport) viewport.scale = scale;
		viewport?.emit('resize');
		window.emit('resize');
		flush();
	};
	return {root, document, window, viewport, focus, resize, flush,
		open: () => root.dataset.phoneKeyboard === 'open'};
}

test('phone navigation follows keyboard occlusion and dismissal with focus retained', () => {
	const f = fixture();
	f.focus();
	assert.equal(f.open(), false);
	f.resize({height: 480});
	assert.equal(f.open(), true);
	assert.equal(f.root.style['--phone-viewport-height'], '480px');
	f.resize({height: 800});
	assert.equal(f.open(), false);
	assert.ok(f.document.activeElement, 'dismissal does not need to blur the editor');
});

test('hardware keyboards and browser toolbar changes leave navigation visible', () => {
	const f = fixture();
	f.focus();
	f.resize({height: 710});
	assert.equal(f.open(), false);
});

test('Android layout viewport resizing retains the height before editing', () => {
	const f = fixture();
	f.focus();
	f.resize({height: 470, layoutHeight: 470});
	assert.equal(f.open(), true);
	f.resize({height: 800, layoutHeight: 800});
	assert.equal(f.open(), false);
});

test('viewport reduction without an editable focus never hides navigation', () => {
	const f = fixture();
	f.resize({height: 400});
	assert.equal(f.open(), false);
	for (const type of ['checkbox', 'radio', 'button', 'range', 'date', 'file']) {
		f.focus('INPUT', {type});
		assert.equal(f.open(), false, type);
	}
	f.focus('TEXTAREA', {readOnly: true});
	assert.equal(f.open(), false);
	f.focus('INPUT', {disabled: true});
	assert.equal(f.open(), false);
});

test('textarea, contenteditable and programmatic focus support keyboard detection', () => {
	for (const [tagName, extra] of [['TEXTAREA', {}], ['DIV', {isContentEditable: true}], ['INPUT', {type: 'email'}]]) {
		const f = fixture();
		f.resize({height: 480});
		f.focus(tagName, extra);
		assert.equal(f.open(), true, tagName);
	}
});

test('pinch zoom does not masquerade as a keyboard or resize the menu bound', () => {
	const f = fixture();
	f.focus();
	f.resize({height: 400, scale: 2});
	assert.equal(f.open(), false);
	assert.equal(f.root.style['--phone-viewport-height'], undefined);
	f.resize({height: 800, scale: 1});
	assert.equal(f.open(), false);
	f.resize({height: 480});
	assert.equal(f.open(), true);
});

test('orientation and layout width changes discard a stale height baseline', () => {
	const f = fixture();
	f.focus();
	f.resize({height: 480});
	assert.equal(f.open(), true);
	f.resize({height: 390, layoutHeight: 390, width: 580});
	assert.equal(f.open(), false);
	f.resize({height: 190, layoutHeight: 190});
	assert.equal(f.open(), true);
	f.window.emit('orientationchange');
	f.flush();
	assert.equal(f.open(), false);
	f.resize({height: 390, layoutHeight: 390});
	f.resize({height: 190, layoutHeight: 190});
	assert.equal(f.open(), true);
	f.resize({viewportWidth: 560});
	assert.equal(f.open(), false, 'unzoomed visual viewport width changes reset the baseline too');
});

test('desktop widths, other phone profiles and missing VisualViewport remain visible', () => {
	for (const options of [{width: 601}, {phone: 'off'}, {visualViewport: false}]) {
		const f = fixture(options);
		f.focus();
		f.resize({height: 400, layoutHeight: 400});
		assert.equal(f.open(), false);
		assert.equal(f.root.style['--phone-viewport-height'], undefined);
	}
});

test('focus removal and HTMX swaps clear keyboard state', () => {
	const f = fixture();
	f.focus();
	f.resize({height: 480});
	assert.equal(f.open(), true);
	f.document.activeElement = null;
	f.document.emit('focusout');
	f.flush();
	assert.equal(f.open(), false);
	f.focus();
	assert.equal(f.open(), true);
	f.document.activeElement = null;
	f.document.emit('htmx:afterSwap');
	f.flush();
	assert.equal(f.open(), false);
});
