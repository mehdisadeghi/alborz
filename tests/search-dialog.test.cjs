const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

// Run the entire search-dialog module with its browser events and dialog geometry.
const helpers = fs.readFileSync(path.join(__dirname, '../templates/assets/helpers.js'), 'utf8');
const marker = "// The header's search, as the reader types.";
assert.notEqual(helpers.indexOf(marker), -1, 'the search-dialog module exists');
const search = helpers.slice(helpers.indexOf(marker), helpers.lastIndexOf('// @license-end'));

function fixture() {
	const listeners = new Map();
	const child = {closest: () => null};
	const dialog = {
		open: true,
		closed: 0,
		querySelector: () => child,
		getBoundingClientRect: () => ({left: 100, right: 500, top: 50, bottom: 400}),
		close() { this.open = false; this.closed++; },
		closest: () => null,
	};
	const document = {
		getElementById: id => id === 'search-dialog' ? dialog : null,
		addEventListener(name, listener) {
			listeners.set(name, [...(listeners.get(name) || []), listener]);
		},
	};
	vm.runInNewContext(search, {document}, {filename: 'helpers.js:search-dialog'});
	return {
		dialog, child,
		click(x, y, target = dialog) {
			for (const listener of listeners.get('click') || []) listener({target, clientX: x, clientY: y});
		},
	};
}

test('a click on any side of the search backdrop dismisses the dialog', () => {
	for (const [x, y] of [[99, 100], [500, 100], [200, 49], [200, 400]]) {
		const f = fixture();
		f.click(x, y);
		assert.equal(f.dialog.closed, 1, `backdrop click at ${x}, ${y}`);
	}
});

test('dialog padding and contents retain the search dialog', () => {
	const f = fixture();
	f.click(100, 50);
	f.click(499, 399);
	f.click(200, 100, f.child);
	assert.equal(f.dialog.open, true);
	assert.equal(f.dialog.closed, 0);
});

test('a closed dialog does not process a backdrop click', () => {
	const f = fixture();
	f.dialog.open = false;
	f.click(10, 10);
	assert.equal(f.dialog.closed, 0);
});
