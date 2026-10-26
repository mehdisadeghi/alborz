const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, '../templates/assets/recipients.js'), 'utf8');
class Element extends EventTarget {
	constructor() { super(); this.children = []; this.attributes = {}; this.hidden = false; }
	setAttribute(key, value) { this.attributes[key] = value; }
	getAttribute(key) { return this.attributes[key]; }
	removeAttribute(key) { delete this.attributes[key]; }
	append(...children) { this.children.push(...children); children.forEach(child => child.parent = this); }
	replaceWith(node) { this.replacement = node; }
	replaceChildren(...children) { this.children = []; this.append(...children); }
	contains(node) { return this === node || this.children.some(child => child.contains(node)); }
	scrollIntoView() {}
	setRangeText(value, start, end) { this.value = this.value.slice(0, start) + value + this.value.slice(end); this.selectionStart = start + value.length; }
}
function fixture(addresses = ['Alice <alice@example.org>', '"Smith, Bob" <bob@example.org>', 'Carol <carol@example.org>']) {
	const document = new Element();
	const input = new Element();
	input.id = 'to'; input.value = ''; input.selectionStart = 0;
	const form = new Element(); form.append(input); form.querySelectorAll = () => [input];
	document.getElementById = id => id === 'compose-form' ? form : {options: addresses.map(value => ({value}))};
	document.createElement = () => new Element();
	const window = new Element();
	vm.runInNewContext(script, {document, window, AbortController, Event});
	const menu = input.replacement.children[1];
	const fire = (target, type, props = {}) => { const event = new Event(type, {cancelable: true}); Object.assign(event, props); target.dispatchEvent(event); return event; };
	return {input, menu, form, document, window, fire,
		type(value, caret = value.length) { input.value = value; input.selectionStart = caret; fire(input, 'input'); },
		key(key, extra = {}) { return fire(input, 'keydown', {key, ...extra}); },
	};
}
test('suggestions stay closed until typing, bounded to eight text-only results', () => {
	const f = fixture(Array.from({length: 20}, (_, i) => `Person ${i} <p${i}@example.org>`));
	assert.equal(f.menu.hidden, true);
	f.type('person');
	assert.equal(f.menu.children.length, 8);
	assert.equal(f.input.getAttribute('aria-expanded'), 'true');
	assert.equal(f.menu.children[0].textContent, 'Person 0 <p0@example.org>');
	f.type('unknown'); assert.equal(f.menu.hidden, true);
});
test('keyboard completion replaces the token at the caret and preserves quoted names and later recipients', () => {
	const f = fixture();
	f.type('"Smith, Bob" <bob@example.org>, ali, carol@example.org', 34);
	assert.equal(f.menu.children.length, 1);
	assert.equal(f.key('ArrowDown').defaultPrevented, true);
	assert.equal(f.input.getAttribute('aria-activedescendant'), 'to-suggestions-0');
	assert.equal(f.key('Enter').defaultPrevented, true);
	assert.equal(f.input.value, '"Smith, Bob" <bob@example.org>, Alice <alice@example.org>, carol@example.org');
	assert.equal(f.menu.hidden, true);
});
test('Escape, blur, outside pointer and HTMX swaps close the menu', () => {
	const f = fixture();
	for (const close of [() => f.key('Escape'), () => f.fire(f.input, 'blur'), () => f.fire(f.document, 'pointerdown'), () => f.fire(f.document, 'htmx:beforeSwap'), () => f.fire(f.window, 'pagehide')]) {
		f.type('ali'); assert.equal(f.menu.hidden, false); close();
		assert.equal(f.menu.hidden, true);
		assert.equal(f.input.getAttribute('aria-activedescendant'), undefined);
	}
});
test('touch selection inserts the address, and IME Enter does not accept a suggestion', () => {
	const f = fixture(); f.type('bob'); f.key('ArrowDown');
	assert.equal(f.key('Enter', {isComposing: true}).defaultPrevented, false);
	assert.equal(f.fire(f.menu.children[0], 'pointerdown').defaultPrevented, true);
	assert.equal(f.input.value, 'bob', 'touch scrolling does not select on pointerdown');
	f.fire(f.menu.children[0], 'click');
	assert.equal(f.input.value, '"Smith, Bob" <bob@example.org>');
	assert.equal(f.menu.hidden, true);
});
test('cleanup releases document handlers so a removed compose cannot react to later swaps', () => {
	const f = fixture(); f.type('ali');
	// The browser bubbles cleanup from the element being removed.
	const cleanup = new Event('htmx:beforeCleanupElement');
	Object.defineProperty(cleanup, 'target', {value: f.form});
	f.document.dispatchEvent(cleanup);
	f.fire(f.document, 'htmx:beforeSwap');
	assert.equal(f.menu.hidden, false);
});
