const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, '../templates/assets/tour.js'), 'utf8');

function fixture(mobile) {
	const events = {};
	const requests = [];
	const step = {dataset: {}, querySelector: () => ({focus() {}})};
	const tour = {open: true, hidden: false, dataset: {seen: '/tour/seen'},
		querySelector: () => step, querySelectorAll: () => [step],
		classList: {toggle() {}}, style: {removeProperty() {}},
		removeAttribute(name) { if (name === 'open') this.open = false; }};
	const location = {href: 'https://example.test/?tour=welcome'};
	vm.runInNewContext(script, {
		CSS: {supports: () => true},
		matchMedia: query => ({matches: mobile && query.includes('pointer: coarse')}),
		document: {getElementById: () => tour, body: {contains: () => true},
			addEventListener(type, fn) { events[type] = fn; }},
		addEventListener() {}, location, URL, WeakSet,
		history: {state: {}, replaceState(state, title, url) { location.href = String(url); }},
		fetch(url, options) { requests.push({url, options}); return Promise.resolve({ok: true}); },
		console,
	});
	return {tour, location, requests, load() { events['htmx:load']({detail: {elt: {contains: () => true}}}); }};
}

test('mobile suppresses the tour without acknowledging it, including HTMX loads', () => {
	const f = fixture(true);
	assert.equal(f.tour.open, false);
	assert.equal(f.tour.hidden, true);
	assert.equal(new URL(f.location.href).searchParams.has('tour'), false);
	f.load();
	assert.equal(f.requests.length, 0);
});

test('a desktop acknowledges a displayed tour only once per dialog', () => {
	const f = fixture(false);
	assert.equal(f.tour.open, true);
	assert.equal(f.requests.length, 1);
	assert.equal(f.requests[0].url, '/tour/seen');
	assert.equal(f.requests[0].options.method, 'POST');
	assert.equal(f.requests[0].options.headers['Alborz-Unasked'], '1');
	f.load();
	assert.equal(f.requests.length, 1);
});
