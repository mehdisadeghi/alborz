const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, '../templates/assets/print.js'), 'utf8');

function fixture(html = true) {
	const button = new EventTarget();
	const frame = {
		style: {height: '2400px'},
		contentDocument: {body: {}, documentElement: {cloneNode: () => ({textContent: 'Full message and signature', querySelectorAll: () => []})}},
		after(element) { this.printCopy = element; },
	};
	const document = new EventTarget();
	document.getElementById = id => id === 'print' ? button : html ? frame : null;
	document.createElement = () => ({
		style: {},
		attachShadow() { return this.shadow = {append: (...children) => { this.children = children; }}; },
		remove() { delete frame.printCopy; },
	});
	const window = new EventTarget();
	let printed = 0;
	window.print = () => { printed++; };
	vm.runInNewContext(script, {document, window, AbortController});
	return {frame, window, document, button, get printed() { return printed; }};
}

test('printing puts the full sanitized body in page flow without changing its screen frame', () => {
	const f = fixture();
	f.window.dispatchEvent(new Event('beforeprint'));
	assert.equal(f.frame.printCopy?.children.at(-1).textContent, 'Full message and signature');
	assert.equal(f.frame.style.height, '2400px');
	const copy = f.frame.printCopy;
	f.window.dispatchEvent(new Event('beforeprint'));
	assert.equal(f.frame.printCopy, copy, 'repeated print events reuse the copy');
	f.window.dispatchEvent(new Event('afterprint'));
	assert.equal(f.frame.printCopy, undefined);
});

test('plain text keeps the ordinary print action', () => {
	const f = fixture(false);
	f.window.dispatchEvent(new Event('beforeprint'));
	f.button.dispatchEvent(new Event('click', {cancelable: true}));
	assert.equal(f.printed, 1);
	f.window.dispatchEvent(new Event('afterprint'));
});

test('leaving the message removes its global print handlers', () => {
	const f = fixture();
	f.window.dispatchEvent(new Event('beforeprint'));
	assert.ok(f.frame.printCopy);
	const cleanup = new Event('htmx:beforeCleanupElement');
	cleanup.detail = {elt: {contains: element => element === f.button}};
	f.document.dispatchEvent(cleanup);
	f.window.dispatchEvent(new Event('beforeprint'));
	assert.equal(f.frame.printCopy, undefined);
});

test('an HTML frame that has not loaded keeps the native print fallback', () => {
	const f = fixture();
	f.frame.contentDocument = null;
	f.window.dispatchEvent(new Event('beforeprint'));
	f.button.dispatchEvent(new Event('click', {cancelable: true}));
	assert.equal(f.frame.printCopy, undefined);
	assert.equal(f.printed, 1);
});
