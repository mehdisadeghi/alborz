const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../templates/assets/passkeys.js'), 'utf8');

test('only a successful server assertion clears the shared session lock', async () => {
	for (const outcome of ['cancelled', 'refused', 'accepted']) {
		let click, calls = 0, cleared = 0, failure = 0, target;
		const button = {dataset: {begin: '/unlock/begin', finish: '/unlock/finish', next: '/mail', failed: 'refused'}, addEventListener(_, fn) { click = fn; }};
		vm.runInNewContext(source, {
			document: {getElementById(id) { return id === 'passkey-unlock' ? button : id === 'script-failed' ? {close() {}} : null; }},
			window: {PublicKeyCredential: {}, AlborzLock: {unlocked() { cleared++; }}},
			navigator: {credentials: {async get() { if (outcome === 'cancelled') throw Error('cancelled'); return {id: 'id', rawId: new Uint8Array([1]).buffer, type: 'public-key', response: {}}; }}},
			fetch: async () => ++calls === 1 ? {ok: true, json: async () => ({publicKey: {challenge: 'YQ'}})} : {ok: outcome === 'accepted', statusText: 'Forbidden'},
			showScriptFailure() { failure++; }, location: {assign(url) { target = url; }},
			Uint8Array, atob: text => Buffer.from(text, 'base64').toString('binary'), btoa: text => Buffer.from(text, 'binary').toString('base64'),
		}, {filename: 'passkeys.js'});
		await click();
		assert.equal(cleared, outcome === 'accepted' ? 1 : 0, outcome);
		assert.equal(failure, outcome === 'accepted' ? 0 : 1, outcome);
		assert.equal(target, outcome === 'accepted' ? '/mail' : undefined, outcome);
	}
});
