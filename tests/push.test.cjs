const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, '../templates/assets/push.js'), 'utf8');

function fixture(permission = 'granted') {
	const failures = [];
	const calls = {permission: 0, subscribe: 0, close: 0};
	const forms = ['one@example.org', 'two@example.org'].map(account => ({
		elements: Object.fromEntries(Object.entries({account, endpoint: '', p256dh: '', auth: ''}).map(([key, value]) => [key, {value}])),
		addEventListener(name, listener) { this.listener = listener; },
		submit() { this.submitted = true; },
	}));
	let subscription;
	const notification = {
		permission,
		async requestPermission() { calls.permission++; return this.permission; },
	};
	const pushManager = {
		async getSubscription() { return subscription; },
		async subscribe() {
			calls.subscribe++;
			subscription = {toJSON: () => ({endpoint: 'https://push.example.org/browser', keys: {p256dh: 'key', auth: 'auth'}})};
			return subscription;
		},
	};
	const settings = {dataset: {key: 'a2V5', denied: 'denied', failed: 'failed', unsupported: 'unsupported'}, querySelectorAll: () => forms};
	vm.runInNewContext(script, {
		document: {getElementById: () => settings},
		navigator: {serviceWorker: {ready: Promise.resolve({pushManager})}},
		window: {PushManager: {}, Notification: notification}, Notification: notification,
		showScriptFailure: text => failures.push(text), failedNotice: () => ({close: () => calls.close++}),
		atob: text => Buffer.from(text, 'base64').toString('binary'), Uint8Array,
	}, {filename: 'push.js'});
	return {forms, failures, calls, notification, pushManager, submit: i => forms[i].listener({currentTarget: forms[i], preventDefault() {}})};
}

test('accounts reuse the browser subscription and retain their explicit account', async () => {
	const f = fixture();
	await f.submit(0);
	await f.submit(1);
	assert.equal(f.calls.permission, 0);
	assert.equal(f.calls.subscribe, 1);
	assert.deepEqual(f.forms.map(form => form.elements.account.value), ['one@example.org', 'two@example.org']);
	for (const form of f.forms) {
		assert.equal(form.submitted, true);
		assert.equal(form.elements.endpoint.value, 'https://push.example.org/browser');
		assert.equal(form.elements.p256dh.value, 'key');
		assert.equal(form.elements.auth.value, 'auth');
	}
});

test('denied permission leaves the form retryable', async () => {
	const f = fixture('denied');
	await f.submit(0);
	assert.deepEqual(f.failures, ['denied']);
	assert.equal(f.forms[0].submitted, undefined);
	assert.equal(f.forms[0].elements.endpoint.value, '');
	f.notification.permission = 'granted';
	await f.submit(0);
	assert.equal(f.forms[0].submitted, true);
	assert.equal(f.calls.close, 1);
});

test('permission and subscription errors show a failure without submitting', async () => {
	for (const stage of ['permission', 'subscription']) {
		const f = fixture(stage === 'permission' ? 'default' : 'granted');
		if (stage === 'permission') f.notification.requestPermission = async () => { throw Error('permission failure'); };
		else f.pushManager.subscribe = async () => { throw Error('subscription failure'); };
		await f.submit(0);
		assert.deepEqual(f.failures, ['failed']);
		assert.equal(f.forms[0].submitted, undefined);
	}
});
