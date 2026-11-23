const assert = require('node:assert/strict');
const test = require('node:test');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const helpers = fs.readFileSync(path.join(__dirname, '../templates/assets/helpers.js'), 'utf8');
const start = helpers.indexOf('// CSS hides the secondary calendar');
const end = helpers.indexOf('// A destination picker opens', start);
assert.ok(start >= 0 && end > start);

function settings(primary, secondary) {
	const listeners = [];
	const calendar = {value: primary, addEventListener(type, fn) { if (type === 'change') listeners.push(fn); }};
	const select = {value: secondary, dataset: {excludes: 'calendar'}, form: {elements: {calendar}}};
	vm.runInNewContext(helpers.slice(start, end), {document: {querySelectorAll: () => [select]}, once: () => true});
	return {select, choose(value) { calendar.value = value; listeners.forEach(fn => fn()); }};
}

test('a secondary calendar the primary takes falls back to none', () => {
	const s = settings('gregorian', 'shcal');
	s.choose('shcal');
	assert.equal(s.select.value, '');
});

test('a secondary calendar the primary leaves alone stays', () => {
	const s = settings('shcal', 'gregorian');
	s.choose('shcal');
	assert.equal(s.select.value, 'gregorian');
});
