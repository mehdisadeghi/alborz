const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, '../templates/assets/editor.js'), 'utf8');

// The compose form as the server draws it: the html field disabled, so only the editor sends it.
function compose(html) {
	const on = el => Object.assign(el, {listeners: {}, addEventListener(type, fn) { (el.listeners[type] ??= []).push(fn); }});
	const htmlInput = {value: html, disabled: true};
	const textarea = {value: 'Text', defaultValue: 'Text', hidden: false, dataset: {caret: 'end'}, hasAttribute: () => false};
	const holder = on({hidden: true, children: [], innerHTML: ''});
	const toolbar = on({classList: {add() {}, remove() {}}, querySelectorAll: () => []});
	const toggle = on({setAttribute() {}});
	const parts = {'textarea.body': textarea, '.editor': holder, '.editor-toolbar': toolbar, 'input[name=html]': htmlInput, '[data-editor-toggle]': toggle};
	const form = on({dataset: {localImage: '^/compose/'}, querySelector: selector => parts[selector]});
	class Squire {
		setHTML(value) { this.html = value; }
		getHTML() { return this.html; }
		addEventListener() {}
		hasFormat() { return false; }
		moveCursorToEnd() {}
		focus() {}
		destroy() {}
	}
	vm.runInNewContext(script, {
		window: {Squire}, Squire, matchMedia: () => ({matches: false}),
		document: {getElementById: id => id === 'compose-form' ? form : null},
	}, {filename: 'editor.js'});
	return {
		htmlInput,
		submit() { form.listeners.submit.forEach(fn => fn()); },
		toggle() { toggle.listeners.click.forEach(fn => fn({preventDefault() {}})); },
	};
}

test('the html field is sent while the editor is open, and not once it closes', () => {
	const f = compose('<p>Rich</p>');
	assert.equal(f.htmlInput.disabled, false, 'a draft opened in the editor sends its HTML');
	f.submit();
	assert.equal(f.htmlInput.value, '<p>Rich</p>');
	f.toggle();
	assert.equal(f.htmlInput.disabled, true, 'plain text sends no HTML');
});

test('a plain message sends no HTML', () => {
	const f = compose('');
	f.submit();
	assert.equal(f.htmlInput.disabled, true);
});
