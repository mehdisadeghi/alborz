const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const helpers = fs.readFileSync(path.join(__dirname, '../templates/assets/helpers.js'), 'utf8');
const start = helpers.indexOf('// A form is sent once.');
const end = helpers.indexOf('// One colours menu', start);
assert.ok(start >= 0 && end > start);

function fixture() {
	const listeners = new Map(), timers = [];
	const attributes = new Map();
	const button = {disabled: false, dataset: {}};
	const form = {
		tagName: 'FORM', dataset: {},
		querySelectorAll: () => [button],
		setAttribute: (key, value) => attributes.set(key, value),
		removeAttribute: key => attributes.delete(key),
	};
	const classes = new Set();
	const bar = {hidden: true, classList: {
		add: value => classes.add(value), remove: value => classes.delete(value),
	}};
	const on = (name, fn) => listeners.set(name, [...(listeners.get(name) || []), fn]);
	const document = {
		addEventListener: on,
		querySelector: () => bar,
		querySelectorAll: selector => selector === 'form[data-busy]' ? [form] : [button],
	};
	vm.runInNewContext(helpers.slice(start, end), {
		document, window: {addEventListener: on}, unasked: 'Alborz-Unasked',
		setTimeout: fn => { timers.push(fn); return timers.length; }, clearTimeout() {},
		requestAnimationFrame: fn => fn(),
	});
	const dispatch = (name, headers = {}) => {
		const event = {target: form, persisted: true, detail: {
			elt: form, target: form, requestConfig: {headers: {}},
			xhr: {getResponseHeader: name => headers[name] || null},
		}};
		for (const fn of listeners.get(name) || []) fn(event);
	};
	return {button, form, attributes, bar, dispatch, flush() {
		for (const fn of timers.splice(0)) fn();
	}};
}

test('successful full-page redirect stays busy while the destination is loading', () => {
	const f = fixture();
	f.dispatch('htmx:beforeRequest');
	f.flush();
	assert.equal(f.button.disabled, true);
	f.dispatch('htmx:afterRequest', {'HX-Redirect': '/mailbox/INBOX'});
	f.flush();
	assert.equal(f.form.dataset.redirecting, '1');
	assert.equal(f.attributes.get('aria-busy'), 'true');
	assert.equal(f.button.disabled, true);
	assert.equal(f.bar.hidden, false);
});

test('normal responses and request failures restore submission controls', () => {
	for (const result of ['htmx:afterRequest', 'htmx:sendError', 'htmx:timeout']) {
		const f = fixture();
		f.dispatch('htmx:beforeRequest');
		f.flush();
		// htmx emits afterRequest as well as the specific transport error.
		f.dispatch('htmx:afterRequest');
		if (result !== 'htmx:afterRequest') f.dispatch(result);
		f.flush();
		assert.equal(f.button.disabled, false, result);
		assert.equal(f.attributes.has('aria-busy'), false, result);
		assert.equal(f.bar.hidden, true, result);
	}
});

test('Back restoration clears the redirect state and re-enables the form', () => {
	const f = fixture();
	f.dispatch('htmx:beforeRequest');
	f.flush();
	f.dispatch('htmx:afterRequest', {'HX-Redirect': '/mailbox/INBOX'});
	f.dispatch('pageshow');
	assert.equal(f.form.dataset.redirecting, undefined);
	assert.equal(f.attributes.has('aria-busy'), false);
	assert.equal(f.button.disabled, false);
	assert.equal(f.bar.hidden, true);
});
