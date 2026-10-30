const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const config = {version: 'v1', shell: [], downloads: [], signings: [], signIn: '/login', assets: '/assets/',
	icon: '/icon.png', pushKey: 'key', renew: '/subscribe', shellHeader: 'X-Shell'};
const source = `const CONFIG = ${JSON.stringify(config)};\n`
	+ fs.readFileSync(path.join(__dirname, '../web/serviceworker.js'), 'utf8');

function fixture(fetchResult) {
	let releaseCache, requests = 0;
	const deleted = [], pending = [], listeners = {};
	const cache = new Promise(resolve => { releaseCache = resolve; });
	vm.runInNewContext(source, {
		self: {location: {origin: 'https://mail.example'}, addEventListener(name, listener) { listeners[name] = listener; },
			clients: {async get() { return {postMessage() {}}; }}},
		caches: {open: () => cache, async delete(name) { deleted.push(name); }},
		fetch: async () => { requests++; return fetchResult(); },
		URL, URLSearchParams, Headers, Response, AbortController, setTimeout, clearTimeout,
	});
	let response;
	listeners.fetch({request: {url: 'https://mail.example/message/INBOX/42', method: 'GET', mode: 'navigate', headers: new Headers()},
		clientId: 'tab', respondWith(promise) { response = promise; }, waitUntil(promise) { pending.push(promise); }});
	return {response, deleted, pending, requests: () => requests, release(saved) { releaseCache({async match() { return saved; }}); }};
}

test('live and locked pages start and finish without waiting for cache storage', async () => {
	for (const status of [200, 303]) {
		const f = fixture(() => new Response(status === 200 ? 'locked' : '', {status, headers: {'Cache-Control': 'no-store'}}));
		const answer = await f.response;
		assert.equal(f.requests(), 1);
		assert.equal(answer.status, status);
		await Promise.all(f.pending);
		assert.deepEqual(f.deleted, ['alborz-pages']);
		// A stale body arriving after the no-store answer must never replace it.
		f.release(new Response('private old mail', {headers: {'X-Alborz-Saved': String(Date.now()), 'X-Shell': 'v1'}}));
		assert.equal(await answer.text(), status === 200 ? 'locked' : '');
	}
});

test('an early network rejection waits for the saved page without an unhandled rejection', async () => {
	const f = fixture(() => { throw new Error('offline'); });
	await new Promise(resolve => setImmediate(resolve));
	assert.equal(f.requests(), 1);
	f.release(new Response('saved mail', {headers: {'X-Alborz-Saved': String(Date.now()), 'X-Shell': 'v1'}}));
	assert.equal(await (await f.response).text(), 'saved mail');
});

test('an offline request with no cached page still fails', async () => {
	const f = fixture(() => { throw new Error('offline'); });
	const failed = assert.rejects(f.response, /offline/);
	f.release(undefined);
	await failed;
});
