const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../templates/assets/session-lock.js'), 'utf8');

function events(object = {}) {
	const listeners = {};
	object.addEventListener = (name, fn) => (listeners[name] ||= []).push(fn);
	object.fire = (name, event = {}) => { event.type ??= name; for (const fn of listeners[name] || []) fn(event); };
	return object;
}
function hub() {
	return {values: new Map(), tabs: [], channels: [], locked: false};
}
function fixture(shared = hub(), options = {}) {
	const attributes = new Map();
	const root = {setAttribute: (k, v) => attributes.set(k, v), removeAttribute: k => attributes.delete(k), hasAttribute: k => attributes.has(k)};
	const nodes = {'session-lock-status': {textContent: ''}, 'session-lock-retry': {}};
	const meta = {content: options.session || 'public-session-id', dataset: {after: options.after === undefined ? '600' : options.after,
		private: 'true', lock: '/lock', state: '/unlock/state', unlock: '/unlock', alive: '/alive', setup: '/settings/passkeys/new',
		title: 'Locked', checking: 'Checking', failed: 'Cannot verify. Retry.', retry: 'Retry'}};
	const document = events({documentElement: root, title: 'Private subject', visibilityState: 'visible',
		querySelector: () => meta, getElementById: id => nodes[id]});
	const window = events({});
	const location = {pathname: options.unlock ? '/unlock' : '/compose', search: '', assign(url) { this.assigned = url; }};
	const timers = new Map(); let nextTimer = 0;
	let now = 100000;
	class Clock extends Date { static now() { return now; } }
	const calls = [];
	const localStorage = {
		getItem: key => shared.values.get(key) || null,
		setItem(key, value) { const oldValue = shared.values.get(key) || null; shared.values.set(key, value); if (oldValue !== value) for (const tab of shared.tabs) if (tab !== window) tab.fire('storage', {key, newValue: value}); },
		removeItem(key) { const had = shared.values.delete(key); if (had) for (const tab of shared.tabs) if (tab !== window) tab.fire('storage', {key, newValue: null}); },
	};
	class BroadcastChannel {
		constructor(name) { this.name = name; shared.channels.push(this); }
		postMessage(data) { for (const channel of shared.channels) if (channel !== this && channel.name === this.name) channel.onmessage?.({data}); }
	}
	const fetch = async (url, init) => {
		calls.push({url, init});
		if (url === '/lock') { if (options.failLock) throw Error('offline'); shared.locked = true; return {status: 204, ok: true}; }
		if (url === '/alive') return {status: 204, ok: true};
		if (options.state) return options.state();
		return {status: 200, ok: true, json: async () => ({session: meta.content, protected: true, locked: shared.locked, after: 600})};
	};
	shared.tabs.push(window);
	vm.runInNewContext(source, {window, document, location, localStorage, fetch, Date: Clock, URL,
		BroadcastChannel: options.noBroadcast ? undefined : BroadcastChannel,
		setTimeout(fn) { const id = ++nextTimer; timers.set(id, fn); return id; }, clearTimeout: id => timers.delete(id)}, {filename: 'session-lock.js'});
	const f = {window, document, location, nodes, calls, shared, options, timers, hidden: () => root.hasAttribute('data-session-concealed')};
	f.advance = milliseconds => { now += milliseconds; };
	document.fire('DOMContentLoaded');
	return f;
}
const settle = () => new Promise(resolve => setImmediate(resolve));

test('protected pages conceal before paint and reveal only after current-session confirmation', async () => {
	let resolve;
	const f = fixture(hub(), {state: () => new Promise(done => { resolve = done; })});
	assert.equal(f.hidden(), true);
	resolve({ok: true, json: async () => ({session: 'different-session', protected: false, locked: false})});
	await settle();
	assert.equal(f.hidden(), true);
	assert.ok(f.location.assigned.startsWith('/unlock'));
});

test('confirmed manual lock conceals other tabs through storage even with no stream or BroadcastChannel', async () => {
	const shared = hub();
	const a = fixture(shared, {noBroadcast: true}); const b = fixture(shared, {noBroadcast: true});
	await settle(); assert.equal(b.hidden(), false);
	await a.window.AlborzLock.lock();
	assert.equal(a.hidden(), true); assert.equal(b.hidden(), true);
	assert.ok(b.location.assigned.startsWith('/unlock'));
	assert.equal(b.document.title, 'Locked');
	assert.ok(shared.values.get('alborz-locked:public-session-id'));
});

test('idle expiry checks the shared server state and does not lock an active sibling tab', async () => {
	const f = fixture(); await settle();
	const timer = [...f.timers.values()].at(-1); timer(); await settle();
	assert.equal(f.hidden(), false); assert.equal(f.shared.locked, false);
	assert.equal(f.shared.values.has('alborz-locked:public-session-id'), false);
});

test('history and visibility restores remain concealed while verification or navigation is stalled', async () => {
	const f = fixture(); await settle();
	f.document.visibilityState = 'hidden'; f.document.fire('visibilitychange'); assert.equal(f.hidden(), true);
	f.options.state = () => new Promise(() => {});
	f.document.visibilityState = 'visible'; f.document.fire('visibilitychange'); await settle(); assert.equal(f.hidden(), true);
	f.window.fire('pagehide'); f.window.fire('pageshow', {persisted: true}); assert.equal(f.hidden(), true);
});

test('failed manual lock stays concealed without telling sibling tabs it succeeded', async () => {
	const shared = hub(); const a = fixture(shared, {failLock: true}); const b = fixture(shared);
	await settle(); await a.window.AlborzLock.lock(); await settle();
	assert.equal(a.hidden(), true); assert.equal(b.hidden(), false);
	assert.equal(shared.values.has('alborz-locked:public-session-id'), false);
	assert.equal(a.nodes['session-lock-status'].textContent, 'Cannot verify. Retry.');
});

test('old checks cannot reveal after a confirmed lock, and only successful unlock removes the marker', async () => {
	const shared = hub(); const a = fixture(shared); await settle();
	let resolve;
	a.options.state = () => new Promise(done => { resolve = done; });
	const checking = a.window.AlborzLock.verify();
	a.window.AlborzLock.locked();
	resolve({ok: true, json: async () => ({session: 'public-session-id', protected: true, locked: false, after: 600})});
	await checking; assert.equal(a.hidden(), true);
	assert.ok(shared.values.get('alborz-locked:public-session-id'));
	const unlock = fixture(shared, {unlock: true});
	assert.ok(shared.values.get('alborz-locked:public-session-id'));
	shared.locked = false; unlock.window.AlborzLock.unlocked();
	assert.equal(shared.values.has('alborz-locked:public-session-id'), false);
});

test('a repeated lock reaches storage-only tabs when an earlier unlock reply was lost', async () => {
	const shared = hub(); const a = fixture(shared, {noBroadcast: true}); const b = fixture(shared, {noBroadcast: true});
	await settle(); await a.window.AlborzLock.lock();
	const previous = shared.values.get('alborz-locked:public-session-id');
	shared.locked = false; await b.window.AlborzLock.verify();
	assert.equal(b.hidden(), false);
	await a.window.AlborzLock.lock();
	assert.notEqual(shared.values.get('alborz-locked:public-session-id'), previous);
	assert.equal(b.hidden(), true);
});

test('a stale manual lock control directs to passkey setup when protection was removed elsewhere', async () => {
	const f = fixture(hub(), {failLock: true}); await settle();
	f.options.state = async () => ({ok: true, json: async () => ({session: 'public-session-id', protected: false, locked: false, after: 0})});
	await f.window.AlborzLock.lock();
	assert.equal(f.location.assigned, '/settings/passkeys/new');
	assert.equal(f.hidden(), true);
});

function emailFrame() {
	return events({isConnected: true, contentDocument: events(), hasAttribute: name => name === 'srcdoc',
		sandbox: {contains: token => ['allow-same-origin', 'allow-popups'].includes(token)}});
}

for (const where of ['page', 'email']) {
	for (const type of ['wheel', 'touchmove', 'keydown', 'pointerdown']) {
		test(`trusted ${where} ${type} renews presence using the existing throttle`, async () => {
			const f = fixture(); await settle();
			const frame = f.nodes['email-frame'] = emailFrame();
			f.document.fire('htmx:load');
			const target = where === 'email' ? frame.contentDocument : f.document;
			f.advance(60000);
			target.fire(type, {isTrusted: true});
			target.fire(type, {isTrusted: true});
			assert.equal(f.calls.filter(call => call.url === '/alive').length, 1);
			f.advance(60000);
			target.fire(type, {isTrusted: true});
			assert.equal(f.calls.filter(call => call.url === '/alive').length, 2);
		});
	}
}

test('synthetic input, unprompted scrolling and background requests do not renew presence', async () => {
	const f = fixture(); await settle();
	const frame = f.nodes['email-frame'] = emailFrame();
	f.document.fire('load', {target: frame});
	f.advance(60000);
	const timer = [...f.timers.keys()].at(-1);
	for (const target of [f.document, frame.contentDocument]) {
		for (const type of ['wheel', 'touchmove', 'keydown', 'pointerdown', 'scroll']) target.fire(type, {isTrusted: false});
		target.fire('scroll', {isTrusted: true});
	}
	f.window.fire('message', {data: {type: 'activity'}, source: frame});
	f.document.fire('htmx:afterRequest', {detail: {requestConfig: {headers: {'Alborz-Unasked': '1'}}}});
	assert.equal(f.calls.filter(call => call.url === '/alive').length, 0);
	assert.equal([...f.timers.keys()].at(-1), timer, 'background events cannot postpone the local idle check');
	f.document.visibilityState = 'hidden';
	frame.contentDocument.fire('wheel', {isTrusted: true});
	f.document.fire('wheel', {isTrusted: true});
	assert.equal(f.calls.filter(call => call.url === '/alive').length, 0);
});

test('scrolling after real input resets the local timer only for a bounded time', async () => {
	const f = fixture(); await settle();
	f.document.fire('wheel', {isTrusted: true});
	let timer = [...f.timers.keys()].at(-1);
	f.advance(100);
	f.document.fire('scroll', {isTrusted: true});
	assert.notEqual([...f.timers.keys()].at(-1), timer);
	timer = [...f.timers.keys()].at(-1);
	f.advance(5000);
	f.document.fire('scroll', {isTrusted: true});
	assert.equal([...f.timers.keys()].at(-1), timer, 'programmatic scrolling cannot keep extending the session');
});

test('email reloads bind the current document without accepting removed or foreign frames', async () => {
	const f = fixture(); await settle();
	const frame = f.nodes['email-frame'] = emailFrame();
	f.document.fire('htmx:load');
	const old = frame.contentDocument;
	frame.contentDocument = events();
	f.document.fire('load', {target: frame});
	f.advance(60000);
	old.fire('wheel', {isTrusted: true});
	assert.equal(f.calls.filter(call => call.url === '/alive').length, 0);
	frame.contentDocument.fire('wheel', {isTrusted: true});
	assert.equal(f.calls.filter(call => call.url === '/alive').length, 1);
	f.advance(60000);
	frame.isConnected = false;
	frame.contentDocument.fire('wheel', {isTrusted: true});
	const foreign = emailFrame();
	f.document.fire('load', {target: foreign});
	foreign.contentDocument.fire('wheel', {isTrusted: true});
	assert.equal(f.calls.filter(call => call.url === '/alive').length, 1);
});
