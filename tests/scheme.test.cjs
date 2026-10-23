const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, '../templates/assets/scheme.js'), 'utf8');

function fixture({scheme = 'system', color = '#272822'} = {}) {
	const events = {};
	const root = {dataset: {}};
	const metas = ['light', 'dark'].map(scheme => ({dataset: {scheme}, content: 'old', media: ''}));
	const system = {addEventListener(type, fn) { events.system = fn; }};
	const context = vm.createContext({
		document: {documentElement: root, readyState: 'loading',
			querySelectorAll: () => metas,
			addEventListener(type, fn) { events[type] = fn; }},
		sessionStorage: {getItem: () => scheme},
		getComputedStyle: () => ({getPropertyValue: () => color}),
		matchMedia: () => system,
	});
	vm.runInContext(script, context);
	return {root, metas, events, setColor(next) { color = next; }, apply(next) { vm.runInContext(`applyScheme(${JSON.stringify(next)})`, context); }};
}

test('initial scheme is applied before styles, then computed theme color replaces metadata', () => {
	const f = fixture({scheme: 'light'});
	assert.equal(f.root.dataset.theme, 'light');
	assert.equal(f.metas[0].content, 'old', 'styles are not loaded yet');
	f.events.DOMContentLoaded();
	assert.equal(f.metas[0].content, '#272822', 'pinned dark theme wins over light override');
	assert.equal(f.metas[0].media, 'all');
	assert.equal(f.metas[1].media, 'not all');
});

test('theme toggles, system changes, and HTMX navigation read the active CSS color', () => {
	const f = fixture();
	f.events.DOMContentLoaded();
	f.setColor('#eee');
	f.apply('light');
	assert.equal(f.metas[0].content, '#eee');
	f.setColor('#111');
	f.events.system();
	assert.equal(f.metas[0].content, '#111');
	f.setColor('#abcdef');
	f.events['htmx:afterSwap']();
	assert.equal(f.metas[0].content, '#abcdef');
});
