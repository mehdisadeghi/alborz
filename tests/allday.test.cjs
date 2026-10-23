const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, '../templates/assets/event-edit.js'), 'utf8');

function form(checked) {
	const listeners = [];
	const fields = Object.fromEntries(['start', 'end', 'start_date', 'end_date', 'until'].flatMap(name => [
		[name, {name, value: name, disabled: false, required: true}],
		[`${name}-calendar`, {name: `${name}-calendar`, value: 'gregorian', disabled: false}],
	]));
	const toggle = {checked, addEventListener(type, fn) { listeners.push(fn); }};
	const eventForm = {elements: {namedItem: name => fields[name]}};
	toggle.form = eventForm;
	return {
		toggle, fields, listeners,
		change(checked) { toggle.checked = checked; listeners.forEach(fn => fn()); },
		submitted() { return Object.values(fields).filter(field => !field.disabled).map(field => field.name); },
		valid() { return Object.values(fields).every(field => field.disabled || !field.required || field.value); },
	};
}

function fixture(checked) {
	const events = {};
	let current = form(checked);
	vm.runInNewContext(script, {
		document: {
			getElementById: id => id === 'allday' ? current?.toggle : null,
			addEventListener(type, fn) { events[type] = fn; },
		},
	}, {filename: 'event-edit.js'});
	return {get current() { return current; }, load(next = current) { current = next; events['htmx:load']?.(); }};
}

for (const checked of [false, true]) {
	test(`initial ${checked ? 'all-day' : 'timed'} form submits only its active dates`, () => {
		const {current: f} = fixture(checked);
		const active = checked ? ['start_date', 'end_date'] : ['start', 'end'];
		assert.deepEqual(f.submitted(), [...active.flatMap(name => [name, `${name}-calendar`]), 'until', 'until-calendar']);
		const inactive = checked ? ['start', 'end'] : ['start_date', 'end_date'];
		inactive.forEach(name => { f.fields[name].value = ''; });
		assert.ok(f.valid(), 'inactive required inputs cannot block submission');
		f.fields[active[0]].value = '';
		assert.equal(f.valid(), false, 'active inputs retain their validation');
	});
}

test('toggling both ways preserves entered values and calendar markers', () => {
	const {current: f} = fixture(false);
	const before = Object.values(f.fields).map(field => field.value);
	f.change(true);
	assert.equal(f.fields.start.disabled, true);
	assert.equal(f.fields.start_date.disabled, false);
	f.change(false);
	assert.equal(f.fields.start.disabled, false);
	assert.equal(f.fields.start_date.disabled, true);
	assert.deepEqual(Object.values(f.fields).map(field => field.value), before);
});

test('HTMX loads initialize new forms and refresh controls without duplicate listeners', () => {
	const f = fixture(false);
	const next = form(true);
	f.load(next);
	assert.equal(next.fields.start.disabled, true);
	next.fields.start = {...next.fields.start, disabled: false};
	f.load();
	assert.equal(next.fields.start.disabled, true, 'controls replaced during enhancement are disabled again');
	assert.equal(next.listeners.length, 1);
	next.change(false);
	assert.equal(next.fields.start.disabled, false);
	assert.equal(next.fields.start_date.disabled, true);
	f.load(null);
	f.load(form(false));
	assert.equal(f.current.fields.start_date.disabled, true, 'returning from another page initializes a fresh form');
});
