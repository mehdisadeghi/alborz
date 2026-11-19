const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const asset = name => fs.readFileSync(path.join(__dirname, '../templates/assets', name), 'utf8');
const picker = asset('emoji.js'), editorScript = asset('editor.js'), list = asset('emoji.txt');
const groupNames = ['recent', 'smileys', 'people', 'animals', 'food', 'travel', 'activities', 'objects', 'symbols', 'flags'];

// Events are plain objects that bubble through parentNode, so a listener on the panel sees the button pressed.
class Element {
	constructor(tagName = 'DIV') {
		Object.assign(this, {tagName, children: [], attributes: {}, dataset: {}, found: {}, listeners: {}, hidden: false, value: ''});
		this.classList = {add() {}, remove() {}};
	}
	append(...nodes) { for (const node of nodes) { if (node instanceof Element) node.parentNode = this; this.children.push(node); } }
	appendChild(node) { this.append(node); return node; }
	replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
	get lastElementChild() { return this.children.at(-1); }
	setAttribute(key, value) { this.attributes[key] = value; }
	hasAttribute(key) { return key in this.attributes; }
	querySelector(selector) { return this.found[selector] ?? null; }
	querySelectorAll(selector) { return this.found[selector] ?? []; }
	closest() { return null; }
	addEventListener(type, fn) { (this.listeners[type] ??= []).push(fn); }
	dispatchEvent(event) {
		event.target ??= this;
		for (let node = this; node; node = node.parentNode) (node.listeners[event.type] ?? []).forEach(fn => fn(event));
	}
	// The caret moves only when asked to, as a text field's does.
	setRangeText(text, start, end, mode) {
		this.value = this.value.slice(0, start) + text + this.value.slice(end);
		if (mode === 'end') this.selectionStart = this.selectionEnd = start + text.length;
	}
}

// The compose page's writing area: the toolbar holds the panel, as compose.html has it.
function fixture({stored, dir = 'ltr', fetch, command = true} = {}) {
	const document = new Element();
	document.documentElement = {dir};
	document.createElement = tag => new Element(tag.toUpperCase());
	Element.prototype.focus = function () { document.activeElement = this; };

	const form = new Element('FORM'), toolbar = new Element(), panel = new Element(), box = new Element();
	const search = new Element('INPUT'), none = new Element('P'), textarea = new Element('TEXTAREA');
	const holder = new Element(), htmlInput = new Element('INPUT'), toggle = new Element('BUTTON');
	const groups = groupNames.map(name => {
		const group = new Element();
		group.dataset.group = name;
		group.hidden = true;
		group.append(new Element('STRONG'), new Element());
		return group;
	});
	none.hidden = true;
	box.append(...groups, none);
	panel.append(search, box);
	panel.dataset.src = '/assets/emoji.txt';
	panel.found = {'input': search, '.emoji-groups': box, '.hint': none, '[data-group]': groups};
	panel.hidePopover = () => { panel.open = false; };
	toolbar.append(toggle, panel);
	form.append(toolbar, holder, textarea);
	form.found = {'textarea.body': textarea, '.editor': holder, '.editor-toolbar': toolbar, 'input[name=html]': htmlInput, '[data-editor-toggle]': toggle};
	document.found = {'compose-form': form, 'menu-emoji': panel};
	document.getElementById = id => document.found[id] ?? null;

	// The browser's insert command, or one that declines as a browser without it would.
	document.execCommand = (name, _ui, text) => {
		const field = document.activeElement;
		if (command) field.setRangeText(text, field.selectionStart, field.selectionEnd, 'end');
		return command;
	};

	const calls = [], storage = new Map(stored === undefined ? [] : [['alborz.emoji', stored]]), fetched = [], notices = [];
	class Squire {
		constructor() { for (const name of ['setHTML', 'addEventListener', 'moveCursorToEnd', 'hasFormat']) this[name] = () => {}; }
		insertPlainText(text, paste) { calls.push(['insertPlainText', text, paste]); }
		focus() { calls.push(['focus']); }
	}
	const context = {
		document, Squire, window: {Squire},
		CustomEvent: class { constructor(type, init) { Object.assign(this, {type}, init); } },
		localStorage: {getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value)},
		matchMedia: () => ({matches: false}),
		fetch: fetch ?? (url => { fetched.push(url); return Promise.resolve({ok: true, text: () => Promise.resolve(list)}); }),
		failedNotice: () => ({querySelector: () => ({dataset: {}, textContent: 'The server did not answer.'})}),
		showScriptFailure: text => notices.push(text),
	};
	vm.runInNewContext(editorScript, context, {filename: 'editor.js'});
	vm.runInNewContext(picker, context, {filename: 'emoji.js'});

	const fire = (target, type, props = {}) => {
		const event = {type, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, ...props};
		target.dispatchEvent(event);
		return event;
	};
	const gridOf = name => groups[groupNames.indexOf(name)].children[1];
	const f = {
		document, panel, search, none, groups, textarea, toggle, calls, fetched, notices, fire, gridOf,
		recent: () => JSON.parse(storage.get('alborz.emoji') ?? '[]'),
		shown: () => groups.filter(group => !group.hidden).flatMap(group => group.children[1].children.filter(b => !b.hidden)),
		named: name => groups.flatMap(group => group.children[1].children).find(b => b.title === name),
		async open() {
			panel.open = true;
			fire(panel, 'toggle', {newState: 'open'});
			await new Promise(setImmediate);
			f.place();
		},
		type(text) { search.value = text; fire(search, 'input'); f.place(); },
		key(key, props) { return fire(document.activeElement, 'keydown', {key, ...props}); },
		// Eight to a row under each group's heading, as the stylesheet lays them out.
		place() {
			let top = 0;
			for (const group of groups.filter(group => !group.hidden)) {
				top += 20;
				group.children[1].children.filter(b => !b.hidden).forEach((b, i) => {
					b.offsetTop = top + 32 * Math.floor(i / 8);
					b.offsetLeft = 32 * (dir === 'rtl' ? 7 - i % 8 : i % 8);
				});
				top += 32 * Math.ceil(group.children[1].children.length / 8);
			}
		},
	};
	return f;
}

test('the list is read when the panel first opens, once, and every emoji is named', async () => {
	const f = fixture();
	assert.deepEqual(f.fetched, [], 'nothing is fetched before the panel opens');
	await f.open();
	await f.open();
	assert.deepEqual(f.fetched, ['/assets/emoji.txt']);
	assert.equal(f.document.activeElement, f.search);
	assert.deepEqual(f.groups.filter(group => !group.hidden).map(group => group.dataset.group), groupNames.slice(1), 'every group but an empty Recently used');
	const all = f.shown();
	assert.ok(all.length > 300);
	assert.equal(new Set(all.map(b => b.textContent)).size, all.length, 'no emoji is listed twice');
	for (const b of all) {
		assert.ok(b.title && b.attributes['aria-label'] === b.title, `${b.textContent} has no accessible name`);
		assert.doesNotMatch(b.textContent, /[‍\u{1F3FB}-\u{1F3FF}]/u, `${b.title} is a joined sequence or carries a skin tone`);
	}
});

test('a search narrows by name and by keyword and says when nothing is found', async () => {
	const f = fixture({stored: '["🎉"]'});
	await f.open();
	f.type('Thumbs  UP');
	assert.deepEqual(f.shown().map(b => b.textContent), ['👍']);
	f.type('happy');
	assert.ok(f.shown().includes(f.named('grinning face')), 'a keyword finds what the name does not');
	f.type('party');
	assert.equal(f.shown().filter(b => b.textContent === '🎉').length, 1, 'a search skips the recent copy');
	assert.equal(f.none.hidden, true);
	f.type('zzzz');
	assert.deepEqual(f.shown(), []);
	assert.equal(f.none.hidden, false);
	await f.open();
	assert.equal(f.search.value, '', 'the next opening starts over');
	assert.equal(f.none.hidden, true);
});

for (const dir of ['ltr', 'rtl']) {
	test(`arrow keys walk the grid from the search field and back (${dir})`, async () => {
		const f = fixture({stored: '["🎉","👍"]', dir});
		await f.open();
		const smileys = f.gridOf('smileys').children;
		const [forward, back] = dir === 'rtl' ? ['ArrowLeft', 'ArrowRight'] : ['ArrowRight', 'ArrowLeft'];
		assert.equal(f.key('ArrowDown').defaultPrevented, true);
		assert.equal(f.document.activeElement.textContent, '🎉', 'down from the search field enters the grid');
		f.key(forward);
		assert.equal(f.document.activeElement.textContent, '👍');
		f.key('ArrowDown');
		assert.equal(f.document.activeElement, smileys[1], 'a short row leads to the same column of the next group');
		f.key('ArrowDown');
		assert.equal(f.document.activeElement, smileys[9]);
		f.key(back);
		assert.equal(f.document.activeElement, smileys[8]);
		f.key(back);
		assert.equal(f.document.activeElement, smileys[7], 'the row before follows the first of a row');
		f.key('ArrowUp');
		assert.equal(f.document.activeElement.textContent, '👍', 'the nearest of a shorter row');
		f.key('ArrowUp');
		assert.equal(f.document.activeElement, f.search, 'up from the first row returns to the search field');
		assert.equal(f.key('a').defaultPrevented, false, 'other keys are the field\'s own');
		// The group's last row is short of the eighth column.
		assert.notEqual(smileys.length % 8, 0);
		smileys[smileys.length - smileys.length % 8 - 1].focus();
		f.key('ArrowDown');
		assert.equal(f.document.activeElement, smileys.at(-1), 'the nearest of the next row, not the same column further down');
	});
}

for (const [mode, key, command] of [['plain text', 'Enter', true], ['plain text', 'click', true], ['plain text', 'click', false], ['rich text', 'click', true]]) {
	test(`a choice by ${key} goes in at the caret of the ${mode}${command ? '' : ' without the insert command'} and the focus follows it`, async () => {
		const f = fixture({command});
		f.textarea.value = 'Hello world';
		f.textarea.selectionStart = f.textarea.selectionEnd = 5;
		if (mode === 'rich text') {
			f.fire(f.toggle, 'click');
		}
		await f.open();
		if (key === 'Enter') {
			assert.equal(f.key('Enter').defaultPrevented, true, 'Enter in an empty search field must not send the message');
			assert.equal(f.panel.open, true);
			f.type('thumbs up');
			assert.equal(f.key('Enter', {isComposing: true}).defaultPrevented, false, 'Enter that ends a composition is the input method\'s');
			assert.equal(f.key('Enter').defaultPrevented, true);
		} else {
			f.fire(f.named('thumbs up'), 'click');
		}
		assert.equal(f.panel.open, false, 'a choice closes the panel');
		if (mode === 'rich text') {
			assert.deepEqual(f.calls, [['insertPlainText', '👍', false], ['focus']]);
			assert.equal(f.textarea.value, 'Hello world');
		} else {
			assert.equal(f.textarea.value, 'Hello👍 world');
			assert.equal(f.textarea.selectionStart, 7);
			assert.equal(f.textarea.selectionEnd, 7);
			assert.equal(f.document.activeElement, f.textarea);
		}
	});
}

test('a selection in the plain text is replaced', async () => {
	const f = fixture();
	f.textarea.value = 'Hello world';
	f.textarea.selectionStart = 0;
	f.textarea.selectionEnd = 5;
	await f.open();
	f.fire(f.named('waving hand'), 'click');
	assert.equal(f.textarea.value, '👋 world');
});

test('recent emoji are kept newest first, without repeats, sixteen at most', async () => {
	const f = fixture({stored: '["🎉","not an emoji","👍"]'});
	await f.open();
	assert.deepEqual(f.gridOf('recent').children.map(b => b.textContent), ['🎉', '👍'], 'what the list does not hold is not shown');
	assert.equal(f.groups[0].hidden, false);
	f.fire(f.named('thumbs up'), 'click');
	assert.deepEqual(f.recent().slice(0, 2), ['👍', '🎉']);
	for (const b of f.gridOf('animals').children.slice(0, 20)) {
		f.fire(b, 'click');
	}
	assert.equal(f.recent().length, 16);
	assert.equal(f.recent()[0], f.gridOf('animals').children[19].textContent);
	await f.open();
	assert.deepEqual(f.gridOf('recent').children.map(b => b.textContent), f.recent());
});

test('what is stored but is no list is ignored', async () => {
	for (const stored of ['{', '"👍"', 'null']) {
		const f = fixture({stored});
		await f.open();
		assert.equal(f.groups[0].hidden, true);
		f.fire(f.named('thumbs up'), 'click');
		assert.deepEqual(f.recent(), ['👍']);
	}
});

test('a list that cannot be read is said and asked for again', async () => {
	let answers = 0;
	const f = fixture({fetch: () => answers++ ? Promise.resolve({ok: true, text: () => Promise.resolve(list)}) : Promise.resolve({ok: false, statusText: 'Bad Gateway'})});
	await f.open();
	assert.deepEqual(f.notices, ['The server did not answer.']);
	assert.deepEqual(f.shown(), []);
	await f.open();
	assert.ok(f.shown().length > 300);
});
