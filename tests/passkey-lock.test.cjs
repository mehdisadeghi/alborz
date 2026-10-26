const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../templates/assets/passkeys.js'), 'utf8');
const settle = () => new Promise(resolve => setImmediate(resolve));
const credential = {id: 'id', rawId: new Uint8Array([1]).buffer, type: 'public-key', response: {}};

function page({create = false, hidden = false, supported = true, outcome = 'accepted', get = async () => credential} = {}) {
	const listeners = {}, actions = {}, requests = [];
	const state = {cleared: 0, registered: 0, failures: 0, prompts: 0};
	const control = {
		dataset: {begin: '/begin', finish: '/finish', next: '/mail', failed: 'refused', unsupported: 'unsupported'},
		disabled: false, elements: {},
		querySelector() { return null; },
		addEventListener(type, fn) { actions[type] = fn; },
	};
	const document = {
		readyState: 'loading', visibilityState: hidden ? 'hidden' : 'visible',
		getElementById(id) { return id === (create ? 'passkey-form' : 'passkey-unlock') ? control : id === 'script-failed' ? {close() {}} : null; },
		addEventListener(type, fn) { (listeners[type] ||= []).push(fn); },
	};
	vm.runInNewContext(source, {
		document,
		window: {PublicKeyCredential: supported ? {} : undefined, AlborzLock: {
			unlocked() { state.cleared++; }, registered() { state.registered++; },
		}, addEventListener: document.addEventListener},
		navigator: {credentials: {
			async get() { state.prompts++; return get(); },
			async create() { state.prompts++; return credential; },
		}},
		fetch: async (url, options) => {
			requests.push({url, options});
			return url === '/begin'
				? {ok: true, json: async () => ({publicKey: {challenge: 'YQ'}})}
				: {ok: outcome === 'accepted', statusText: 'Forbidden'};
		},
		showScriptFailure() { state.failures++; }, location: {assign(url) { state.target = url; }, reload() { state.reloaded = true; }},
		Uint8Array, atob: text => Buffer.from(text, 'base64').toString('binary'), btoa: text => Buffer.from(text, 'binary').toString('base64'),
	}, {filename: 'passkeys.js'});
	return {state, control, requests, document,
		async emit(type, event = {}) { for (const fn of listeners[type] || []) fn(event); await settle(); },
		click: () => actions.click(), submit: () => actions.submit({preventDefault() {}}),
	};
}

test('only a successful server assertion clears the shared session lock', async () => {
	for (const outcome of ['cancelled', 'refused', 'accepted']) {
		const p = page({outcome, get: async () => { if (outcome === 'cancelled') throw Error('cancelled'); return credential; }});
		await p.click();
		assert.equal(p.state.cleared, outcome === 'accepted' ? 1 : 0, outcome);
		assert.equal(p.state.failures, outcome === 'accepted' ? 0 : 1, outcome);
		assert.equal(p.state.target, outcome === 'accepted' ? '/mail' : undefined, outcome);
	}
});

test('opening a visible unlock page prompts without a click and returns to the requested page', async () => {
	const p = page();
	assert.equal(p.state.prompts, 0, 'wait for deferred helpers and page readiness');
	await p.emit('DOMContentLoaded');
	assert.equal(p.state.prompts, 1);
	assert.equal(p.state.cleared, 1);
	assert.equal(p.state.target, '/mail');
});

test('a background unlock page waits until visible and cancellation does not reopen the prompt', async () => {
	let cancel = true;
	const p = page({hidden: true, get: async () => {
		if (cancel) throw Object.assign(new Error('cancelled or gesture required'), {name: 'NotAllowedError'});
		return credential;
	}});
	await p.emit('DOMContentLoaded');
	assert.equal(p.requests.length, 0);
	p.document.visibilityState = 'visible';
	await p.emit('visibilitychange');
	assert.equal(p.state.prompts, 1);
	assert.equal(p.state.failures, 0, 'automatic cancellation is not an error notice');
	assert.equal(p.state.cleared, 0);
	assert.equal(p.control.disabled, false);
	p.document.visibilityState = 'hidden';
	await p.emit('visibilitychange');
	p.document.visibilityState = 'visible';
	await p.emit('visibilitychange');
	assert.equal(p.state.prompts, 1, 'native dialogs can themselves cause visibility changes');
	cancel = false;
	await p.click();
	assert.equal(p.state.cleared, 1, 'manual retry still works');
});

test('clicks during an automatic prompt cannot start competing challenges', async () => {
	let resolve;
	const p = page({get: () => new Promise(done => { resolve = done; })});
	await p.emit('DOMContentLoaded');
	assert.equal(p.control.disabled, true);
	await p.click();
	assert.equal(p.requests.length, 1);
	resolve(credential);
	await settle();
	assert.equal(p.state.cleared, 1);
});

test('visibility changes before page readiness cannot start authentication', async () => {
	const p = page({hidden: true});
	p.document.visibilityState = 'visible';
	await p.emit('visibilitychange');
	assert.equal(p.requests.length, 0, 'deferred error helpers may not be available yet');
	await p.emit('DOMContentLoaded');
	assert.equal(p.state.cleared, 1);
});

test('an automatically refused assertion leaves the session locked and retry available', async () => {
	const p = page({outcome: 'refused'});
	await p.emit('DOMContentLoaded');
	assert.equal(p.state.cleared, 0);
	assert.equal(p.state.failures, 1);
	assert.equal(p.control.disabled, false);
});

test('unsupported browsers keep a manual fallback without an automatic error notice', async () => {
	const p = page({supported: false});
	await p.emit('DOMContentLoaded');
	assert.equal(p.requests.length, 0);
	assert.equal(p.state.failures, 0);
	await p.click();
	assert.equal(p.state.failures, 1);
});

test('registration needs an explicit submit but no passkey name field', async () => {
	const p = page({create: true});
	await p.emit('DOMContentLoaded');
	assert.equal(p.requests.length, 0);
	await p.submit();
	assert.deepEqual(p.requests.map(r => r.url), ['/begin', '/finish']);
	assert.equal(p.state.registered, 1);
	assert.equal(p.state.cleared, 0);
});

test('Back restores revalidate the session instead of keeping a completed ceremony disabled', async () => {
	for (const create of [false, true]) {
		const p = page({create});
		await p.emit('DOMContentLoaded');
		if (create) await p.submit();
		await p.emit('pageshow', {persisted: false});
		assert.equal(p.state.reloaded, undefined);
		await p.emit('pageshow', {persisted: true});
		assert.equal(p.state.reloaded, true);
	}
});
