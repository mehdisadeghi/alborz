const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const helpers = fs.readFileSync(path.join(__dirname, '../templates/assets/helpers.js'), 'utf8');
const start = helpers.indexOf('// A destination picker opens');
const end = helpers.indexOf('const submit_on_change', start);
assert.ok(start >= 0 && end > start);

function picker(saved) {
	const listeners = [], painted = new Map();
	const option = (value, color) => ({value, style: {getPropertyValue: name => name === '--collection' ? color : ''}});
	const select = {
		options: [option('a|/plans/', '#22aa55'), option('a|/work/', '#3366cc'), option('a|/plain/', '')],
		value: 'a|/plans/', dataset: {destinationKey: 'event'},
		style: {setProperty: (name, value) => painted.set(name, value)},
		get selectedOptions() { return this.options.filter(o => o.value === this.value); },
		addEventListener(type, fn) { if (type === 'change') listeners.push(fn); },
		form: {addEventListener() {}},
	};
	const store = new Map(saved ? [['alborz.destination.event', saved]] : []);
	vm.runInNewContext(helpers.slice(start, end), {
		document: {querySelectorAll: () => [select]}, once: () => true,
		localStorage: {getItem: key => store.get(key) ?? null, setItem: (key, value) => store.set(key, value)},
	});
	return {painted, choose(value) { select.value = value; listeners.forEach(fn => fn()); }};
}

test('a destination picker wears the colour of the collection it opens on', () => {
	assert.equal(picker().painted.get('--picked'), '#22aa55');
	assert.equal(picker('a|/work/').painted.get('--picked'), '#3366cc');
});

test('a destination picker follows the chosen collection, to none for one without a colour', () => {
	const p = picker();
	p.choose('a|/work/');
	assert.equal(p.painted.get('--picked'), '#3366cc');
	p.choose('a|/plain/');
	assert.equal(p.painted.get('--picked'), '');
});
