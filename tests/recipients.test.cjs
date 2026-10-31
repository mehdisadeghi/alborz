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
test('keyboard completion replaces the token at the caret and preserves quoted names and later recipients', () => {
	const f = fixture();
	f.type('"Smith, Bob" <bob@example.org>, ali, carol@example.org', 34);
	f.key('ArrowDown');
	f.key('Enter');
	assert.equal(f.input.value, '"Smith, Bob" <bob@example.org>, Alice <alice@example.org>, carol@example.org');
});
test('touch selection inserts the address, and IME Enter does not accept a suggestion', () => {
	const f = fixture(); f.type('bob'); f.key('ArrowDown');
	f.key('Enter', {isComposing: true});
	f.fire(f.menu.children[0], 'pointerdown');
	assert.equal(f.input.value, 'bob', 'IME Enter and touch scrolling do not select');
	f.fire(f.menu.children[0], 'click');
	assert.equal(f.input.value, '"Smith, Bob" <bob@example.org>');
});
