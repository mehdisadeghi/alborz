const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const values = ['v1', '[]', '[]', '[]', '"/login"', '"/assets/"', '"/icon.png"', '"key"', '"/subscribe"', '"X-Shell"'];
const source = fs.readFileSync(path.join(__dirname, '../web/serviceworker.js'), 'utf8')
	.replace(/%\[(\d+)\]s/g, (_, n) => values[Number(n) - 1]);

function fixture(status = 204) {
	const listeners = {}, calls = {shown: [], fetch: [], opened: [], navigated: [], closed: 0};
	const client = {url: 'https://mail.example/inbox', async focus() { return this; }, async navigate(url) { calls.navigated.push(url); }};
	const self = {
		location: {origin: 'https://mail.example'}, navigator: {},
		addEventListener(name, listener) { listeners[name] = listener; },
		registration: {async showNotification(title, options) { calls.shown.push({title, options}); }},
		clients: {async matchAll() { return [client]; }, async openWindow(url) { calls.opened.push(url); }},
	};
	vm.runInNewContext(source, {self, URL, URLSearchParams, AbortController, setTimeout, clearTimeout,
		fetch: async (url, options) => { calls.fetch.push({url, options}); return {status}; }}, {filename: 'serviceworker.js'});
	const note = {title: 'New mail', body: 'Short preview', url: '/message/INBOX/42/notification?account=one&validity=3',
		actions: [{action: 'read', title: 'Mark read', url: '/message/INBOX/42/notification?account=one&validity=3'}]};
	async function click(action = '') {
		let work;
		listeners.notificationclick({action, notification: {data: note, close() { calls.closed++; }}, waitUntil(promise) { work = promise; }});
		await work;
	}
	return {listeners, calls, note, click};
}

test('push carries a preview and browser-supported action buttons', async () => {
	const f = fixture(); let work;
	f.listeners.push({data: {json: () => f.note}, waitUntil(promise) { work = promise; }});
	await work;
	assert.equal(f.calls.shown[0].options.body, 'Short preview');
	assert.equal(f.calls.shown[0].options.actions[0].action, 'read');
	assert.equal(f.calls.shown[0].options.data.url, f.note.url);
});

test('click navigates an existing tab to the exact notification message', async () => {
	const f = fixture(); await f.click();
	assert.deepEqual(f.calls.navigated, ['https://mail.example' + f.note.url]);
	assert.equal(f.calls.fetch.length, 0);
});

test('action posts with session credentials and anti-CSRF header', async () => {
	const f = fixture(); await f.click('read');
	const {url, options} = f.calls.fetch[0];
	assert.equal(url, f.note.actions[0].url);
	assert.equal(options.method, 'POST');
	assert.equal(options.credentials, 'same-origin');
	assert.equal(options.redirect, 'manual');
	assert.equal(options.headers['Alborz-Notification'], '1');
	assert.equal(options.body.get('action'), 'read');
	assert.equal(f.calls.navigated.length, 0);
});

test('locked, signed out or failed actions open the exact message for recovery', async () => {
	for (const status of [0, 303, 403, 409, 500]) {
		const f = fixture(status); await f.click('read');
		assert.deepEqual(f.calls.navigated, ['https://mail.example' + f.note.url]);
	}
});

test('notification metadata cannot navigate or post to another origin', async () => {
	const f = fixture(); f.note.url = 'https://evil.example/'; f.note.actions[0].url = f.note.url;
	await f.click('read');
	assert.equal(f.calls.fetch.length + f.calls.navigated.length + f.calls.opened.length, 0);
});
