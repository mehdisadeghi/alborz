const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

// These fixtures model only the DOM operations used by the gesture handler.
// The production script owns thresholds, action choice, cancellation and submission.
class Element {
	constructor(tag = 'div', attrs = {}) {
		this.tagName = tag.toUpperCase();
		this.attrs = {...attrs};
		this.children = [];
		this.dataset = {};
		for (const [name, value] of Object.entries(attrs)) {
			if (name.startsWith('data-')) this.dataset[name.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = value;
		}
		this.style = {setProperty(name, value) { this[name] = value; }, removeProperty(name) { delete this[name]; }};
		const classes = new Set((attrs.class || '').split(/\s+/).filter(Boolean));
		Object.defineProperty(this, 'className', {get: () => [...classes].join(' '), set: value => { classes.clear(); String(value).split(/\s+/).filter(Boolean).forEach(n => classes.add(n)); }});
		this.classList = {add: (...names) => names.forEach(n => classes.add(n)), remove: (...names) => names.forEach(n => classes.delete(n)),
			contains: n => classes.has(n), toggle(n, force) { const add = force ?? !classes.has(n); if (add) classes.add(n); else classes.delete(n); return add; }};
		this.offsetWidth = 300;
		this.textContent = '';
		this.checked = !!attrs.checked;
		this.disabled = !!attrs.disabled;
		this.name = attrs.name || '';
		this.value = attrs.value || '';
		this.type = attrs.type || '';
		this.id = attrs.id || '';
		this.href = attrs.href || '';
		this.action = attrs.action || '';
		this.method = attrs.method || 'get';
		this.listeners = new Map();
		this.clicks = 0;
		this.hidden = false;
	}
	append(...nodes) { for (const n of nodes) { n.parentElement = this; n.ownerDocument = this.ownerDocument; this.children.push(n); } }
	appendChild(node) { this.append(node); return node; }
	remove() { if (this.parentElement) this.parentElement.children = this.parentElement.children.filter(n => n !== this); this.parentElement = null; }
	replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
	getAttribute(name) { return this.attrs[name] ?? null; }
	setAttribute(name, value) { this.attrs[name] = String(value); if (name === 'id') this.id = String(value); }
	removeAttribute(name) { delete this.attrs[name]; if (name === 'id') this.id = ''; }
	hasAttribute(name) { return name in this.attrs; }
	getBoundingClientRect() { return {left: 30, top: 0, right: 330, bottom: 40, width: 300, height: 40}; }
	get dir() { return this.attrs.dir || ''; }
	set dir(value) { this.attrs.dir = value; }
	get isConnected() { let n = this; while (n.parentElement) n = n.parentElement; return n.tagName === 'DOCUMENT'; }
	matches(selector) {
		return selector.split(',').some(part => {
			part = part.trim();
			if (part.includes(' ')) { const bits = part.split(/\s+/); const last = bits.pop(); return this.matches(last) && !!this.parentElement?.closest(bits.join(' ')); }
			if (/:checked/.test(part) && !this.checked) return false;
			if (/:disabled/.test(part) && !this.disabled) return false;
			const tag = part.match(/^[a-z][\w-]*/i)?.[0];
			if (tag && this.tagName !== tag.toUpperCase()) return false;
			const id = part.match(/#([\w-]+)/)?.[1];
			if (id && this.id !== id) return false;
			for (const [, cls] of part.matchAll(/\.([\w-]+)/g)) if (!this.classList.contains(cls)) return false;
			for (const [, key, operator, value] of part.matchAll(/\[([\w-]+)(?:(\^?=)"?([^\]"']+)"?)?\]/g)) {
				if (!this.hasAttribute(key) || (operator === '=' && this.getAttribute(key) !== value) || (operator === '^=' && !this.getAttribute(key).startsWith(value))) return false;
			}
			return true;
		});
	}
	closest(selector) { for (let n = this; n; n = n.parentElement) if (n.matches(selector)) return n; return null; }
	querySelectorAll(selector) { return this.children.flatMap(n => [...(n.matches(selector) ? [n] : []), ...n.querySelectorAll(selector)]); }
	querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
	addEventListener(name, callback) { this.listeners.set(name, [...(this.listeners.get(name) || []), callback]); }
	removeEventListener(name, callback) { this.listeners.set(name, (this.listeners.get(name) || []).filter(f => f !== callback)); }
	dispatchEvent(event) {
		if (!event.target) event.target = this;
		for (const f of this.listeners.get(event.type) || []) f(event);
		if (event.bubbles !== false && this.parentElement) this.parentElement.dispatchEvent(event);
		return !event.defaultPrevented;
	}
	click() { if (!this.disabled) { this.clicks++; this.dispatchEvent(new FixtureEvent('click', {target: this, bubbles: true, detail: 0})); } }
	contains(node) { return node === this || this.children.some(n => n.contains(node)); }
	setPointerCapture() {}
	releasePointerCapture() {}
	animate() { return {cancel() {}}; }
	get elements() { return this.ownerDocument.querySelectorAll('input,button,select').filter(el => el.form === this || this.contains(el)); }
	requestSubmit(submitter) { this.ownerDocument.submissions.push({form: this, submitter, values: [...new FixtureFormData(this)]}); }
	cloneNode(deep = false) {
		const clone = new Element(this.tagName, this.attrs);
		Object.assign(clone, {checked: this.checked, disabled: this.disabled, value: this.value, textContent: this.textContent});
		if (deep) clone.append(...this.children.map(n => n.cloneNode(true)));
		return clone;
	}
}

class FixtureFormData extends URLSearchParams {
	constructor(form) {
		super();
		for (const el of form?.elements || []) {
			if (!el.name || el.disabled || /^(submit|button)$/.test(el.type) || (/^(checkbox|radio)$/.test(el.type) && !el.checked)) continue;
			this.append(el.name, el.value);
		}
	}
}

class FixtureEvent {
	constructor(type, options = {}) { Object.assign(this, {type, bubbles: false}, options); }
	preventDefault() { this.defaultPrevented = true; }
	stopImmediatePropagation() { this.stopped = true; }
}

function fixture(preferences = {}) {
	const document = new Element('document');
	document.ownerDocument = document;
	document.submissions = [];
	document.readyState = 'complete';
	document.documentElement = new Element('html', {dir: 'ltr'});
	document.body = new Element('body');
	document.append(document.documentElement);
	document.documentElement.append(document.body);
	document.getElementById = id => document.querySelector('#' + id);
	document.createElement = tag => Object.assign(new Element(tag), {ownerDocument: document});
	document.getSelection = () => ({isCollapsed: true, toString: () => ''});
	// The server renders resolved values; defaults are separately covered by Go tests.
	const resolved = {mail: {left: 'delete', right: 'archive'}, tasks: {left: 'delete', right: 'complete'},
		contacts: {left: 'delete', right: 'star'}, journal: {left: 'delete', right: 'star'}, events: {left: 'delete', right: 'none'}, ...preferences};
	const config = new Element('script', {id: 'swipe-preferences', 'data-swipes': JSON.stringify(resolved)});
	document.body.append(config);
	const window = new Element('window');
	Object.assign(window, {innerWidth: 1000, document, getSelection: document.getSelection});
	const stored = new Map();
	const localStorage = {getItem: key => stored.get(key) ?? null, setItem: (key, value) => stored.set(key, String(value)), removeItem: key => stored.delete(key)};
	const processed = [];
	const htmx = {process: form => processed.push(form)};
	const media = () => ({matches: false, addEventListener() {}});
	Object.assign(window, {matchMedia: media, htmx});
	const context = {document, window, localStorage, htmx, FormData: FixtureFormData, Event: FixtureEvent, performance, console, setTimeout, clearTimeout, URL, URLSearchParams,
		innerWidth: 1000, matchMedia: media, getComputedStyle: el => ({display: 'grid', overflowX: el.style.overflowX || 'visible', direction: el.attrs.dir || document.documentElement.attrs.dir || 'ltr'}),
		CSS: {escape: s => s}, Element, HTMLElement: Element, HTMLInputElement: Element, HTMLFormElement: Element,
		location: {href: 'http://test.local/mail', pathname: '/mail'}, navigator: {}, requestAnimationFrame: fn => fn()};
	vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../templates/assets/gestures.js'), 'utf8'), context);
	const pointer = (type, target, x, y = 20, extra = {}) => {
		const event = {type, target, pointerId: 1, pointerType: 'touch', isPrimary: true, button: 0, buttons: 1,
			clientX: x, clientY: y, cancelable: true, preventDefault() { this.defaultPrevented = true; }, ...extra};
		document.dispatchEvent(event);
		return event;
	};
	return {document, window, localStorage, pointer, config, processed};
}

function rowFixture(kind = 'mail', preferences = {}) {
	const f = fixture(preferences);
	const main = new Element('main', {id: 'main'});
	const row = new Element('div', {'data-swipe-kind': kind, class: 'list-row'});
	const primary = new Element('div', {class: 'list-primary'});
	const link = new Element('a', {href: '/message/1'});
	primary.append(link);
	row.append(primary);
	main.append(row);
	f.document.body.append(main);
	const action = (name, attrs = {}) => {
		const button = new Element('button', {'data-swipe-action': name, type: 'submit', ...attrs});
		button.textContent = name;
		row.append(button);
		return button;
	};
	const swipe = (dx, dy = 0, target = link, end = 'pointerup') => {
		f.pointer('pointerdown', target, 200);
		f.pointer('pointermove', target, 200 + dx, 20 + dy);
		return f.pointer(end, target, 200 + dx, 20 + dy);
	};
	return {...f, main, row, link, action, swipe};
}

test('mail defaults act only after release beyond threshold', () => {
	const f = rowFixture();
	const archive = f.action('archive');
	f.pointer('pointerdown', f.link, 200);
	f.pointer('pointermove', f.link, 340);
	assert.equal(archive.clicks, 0, 'moving previews without executing');
	f.pointer('pointerup', f.link, 340);
	assert.equal(archive.clicks, 1);
	f.swipe(35);
	assert.equal(archive.clicks, 1, 'short gesture does not act');
	f.swipe(-140);
	assert.equal(archive.clicks, 1, 'missing delete control cannot fall back to archive');
});

test('section mappings are independent and explicit none disables a side', () => {
	const f = rowFixture('tasks', {mail: {right: 'archive'}, tasks: {left: 'none', right: 'star'}});
	const complete = f.action('complete');
	const star = f.action('star');
	const del = f.action('delete');
	f.swipe(140);
	assert.equal(star.clicks, 1);
	assert.equal(complete.clicks, 0);
	f.swipe(-140);
	assert.equal(del.clicks, 0);
});

test('vertical movement, multiple pointers and cancellation never act', () => {
	const f = rowFixture();
	const archive = f.action('archive');
	f.swipe(140, 180);
	f.swipe(140, 0, f.link, 'pointercancel');
	f.pointer('pointerdown', f.link, 200);
	f.pointer('pointerdown', f.link, 210, 20, {pointerId: 2, isPrimary: false});
	f.pointer('pointermove', f.link, 350);
	f.pointer('pointerup', f.link, 350);
	assert.equal(archive.clicks, 0);
});

test('interactive controls, mouse input, system edges and disabled actions are excluded', () => {
	const f = rowFixture();
	const archive = f.action('archive');
	for (const tag of ['input', 'button', 'select', 'textarea', 'a']) {
		const control = new Element(tag, tag === 'a' ? {href: '/attachment'} : {});
		f.row.append(control);
		f.swipe(140, 0, control);
	}
	f.pointer('pointerdown', f.link, 200, 20, {pointerType: 'mouse'});
	f.pointer('pointermove', f.link, 350, 20, {pointerType: 'mouse'});
	f.pointer('pointerup', f.link, 350, 20, {pointerType: 'mouse'});
	f.pointer('pointerdown', f.link, 10);
	f.pointer('pointermove', f.link, 150);
	f.pointer('pointerup', f.link, 150);
	archive.disabled = true;
	f.swipe(140);
	assert.equal(archive.clicks, 0);
});

test('PIM deletion requires a separate confirmation tap', () => {
	const f = rowFixture('contacts');
	const del = f.action('delete', {'data-swipe-confirm': ''});
	f.swipe(-140);
	assert.equal(del.clicks, 0);
	const confirm = f.main.querySelector('.swipe-confirm');
	assert.ok(confirm, 'a tappable confirmation appears');
	confirm.click();
	assert.equal(del.clicks, 1);
});

function bulkFixture() {
	const f = rowFixture();
	const form = new Element('form', {id: 'messages-form', action: '/mail/action', method: 'post'});
	f.document.body.append(form);
	const input = (attrs, parent = form) => {
		const el = new Element('input', attrs);
		el.form = form;
		parent.append(el);
		return el;
	};
	input({type: 'hidden', name: 'csrf', value: 'token'});
	input({type: 'hidden', name: 'account', value: 'a@test.local'});
	input({type: 'hidden', name: 'next', value: '/inbox'});
	input({type: 'hidden', name: 'everything', value: '', form: 'messages-form'});
	const target = input({type: 'checkbox', name: 'uids', value: '11', form: 'messages-form'}, f.row);
	const otherRow = new Element('div', {'data-swipe-kind': 'mail', class: 'list-row'});
	f.main.append(otherRow);
	const other = input({type: 'checkbox', name: 'uids', value: '22', form: 'messages-form', checked: ''}, otherRow);
	other.checked = true;
	const archive = new Element('button', {'data-swipe-action': 'archive', type: 'submit', formaction: '/archive', form: 'messages-form'});
	archive.form = form;
	archive.formAction = '/archive';
	archive.textContent = 'Archive';
	f.main.append(archive);
	return {...f, form, target, other, otherRow, archive};
}

test('bulk row action submits only that row without changing other selections', () => {
	const f = bulkFixture();
	f.swipe(140);
	assert.equal(f.document.submissions.length, 1);
	const submitted = f.document.submissions[0];
	assert.notEqual(submitted.form, f.form);
	assert.equal(submitted.form.getAttribute('id'), null);
	const values = new URLSearchParams(submitted.values);
	assert.deepEqual(values.getAll('uids'), ['11']);
	assert.equal(values.has('everything'), false);
	assert.equal(values.get('csrf'), 'token');
	assert.equal(values.get('account'), 'a@test.local');
	assert.equal(values.get('next'), '/inbox');
	assert.equal(f.target.checked, false);
	assert.equal(f.other.checked, true);
	assert.ok(f.processed.includes(submitted.form), 'the isolated form is processed by htmx');
});

test('only the matching request restores unrelated selections after a list redraw', () => {
	const f = bulkFixture();
	f.swipe(140);
	const form = f.document.submissions[0].form;
	const xhr = {};
	form.dispatchEvent(new FixtureEvent('htmx:beforeRequest', {detail: {xhr}}));
	f.document.dispatchEvent(new FixtureEvent('htmx:beforeSwap', {detail: {xhr}}));
	f.other.remove();
	const replacement = new Element('input', {type: 'checkbox', name: 'uids', value: '22', form: 'messages-form'});
	replacement.form = f.form;
	f.otherRow.append(replacement);
	f.document.dispatchEvent(new FixtureEvent('htmx:afterSwap', {detail: {xhr: {}}}));
	f.document.dispatchEvent(new FixtureEvent('htmx:afterSettle', {detail: {xhr: {}}}));
	assert.equal(replacement.checked, false, 'another request cannot restore this selection');
	f.document.dispatchEvent(new FixtureEvent('htmx:afterSwap', {detail: {xhr}}));
	assert.equal(replacement.checked, false, 'restoration waits for enhancement and settle');
	f.document.dispatchEvent(new FixtureEvent('htmx:load', {detail: {elt: f.main}}));
	f.document.dispatchEvent(new FixtureEvent('htmx:afterSettle', {detail: {xhr}}));
	assert.equal(replacement.checked, true);
	assert.equal(f.target.checked, false, 'the swiped row never becomes part of the selection');
	f.document.dispatchEvent(new FixtureEvent('htmx:afterRequest', {detail: {xhr, successful: true}}));
	assert.equal(form.isConnected, false, 'the isolated form is cleaned up');
});

test('a failed row request preserves original selections and cleans up its isolated form', () => {
	const f = bulkFixture();
	f.swipe(140);
	const form = f.document.submissions[0].form;
	const xhr = {};
	form.dispatchEvent(new FixtureEvent('htmx:beforeRequest', {detail: {xhr}}));
	f.document.dispatchEvent(new FixtureEvent('htmx:afterRequest', {detail: {xhr, successful: false}}));
	assert.equal(f.other.checked, true);
	assert.equal(f.target.checked, false);
	assert.equal(form.isConnected, false);
});

test('a list redraw preserves whole-list selection without submitting it for the row action', () => {
	const f = bulkFixture();
	f.form.querySelector('input[name="everything"]').value = '1';
	f.target.checked = true;
	f.swipe(140);
	const form = f.document.submissions[0].form;
	assert.equal(new URLSearchParams(f.document.submissions[0].values).has('everything'), false);
	const xhr = {};
	form.dispatchEvent(new FixtureEvent('htmx:beforeRequest', {detail: {xhr}}));
	f.document.dispatchEvent(new FixtureEvent('htmx:beforeSwap', {detail: {xhr}}));
	f.form.remove();
	const replacement = new Element('form', {id: 'messages-form', method: 'post', action: '/mail/action'});
	f.document.body.append(replacement);
	f.document.dispatchEvent(new FixtureEvent('htmx:afterSwap', {detail: {xhr}}));
	f.document.dispatchEvent(new FixtureEvent('htmx:load', {detail: {elt: replacement}}));
	f.document.dispatchEvent(new FixtureEvent('htmx:afterSettle', {detail: {xhr}}));
	assert.equal(new FixtureFormData(replacement).get('everything'), '1');
});

for (const finishesFirst of [false, true]) test(`whole-list selection waits for enhancement when request finishes ${finishesFirst ? 'before' : 'after'} settle`, () => {
	const f = bulkFixture();
	f.form.querySelector('input[name="everything"]').value = '1';
	f.target.checked = true;
	f.swipe(140);
	const isolated = f.document.submissions[0].form;
	const xhr = {};
	isolated.dispatchEvent(new FixtureEvent('htmx:beforeRequest', {detail: {xhr}}));
	f.document.dispatchEvent(new FixtureEvent('htmx:beforeSwap', {detail: {xhr}}));
	f.form.remove();
	f.main.replaceChildren();
	const form = new Element('form', {id: 'messages-form'});
	const everything = new Element('input', {name: 'everything', type: 'hidden', value: '', form: 'messages-form'});
	everything.form = form;
	form.append(everything);
	const box = new Element('input', {type: 'checkbox', name: 'uids', value: '22', form: 'messages-form'});
	box.form = form;
	const offer = new Element('div', {class: 'select-everything', 'data-form': 'messages-form'});
	const widen = new Element('button', {type: 'button', class: 'select-widen'});
	const whole = new Element('span', {class: 'select-whole'});
	whole.hidden = true;
	offer.append(widen, whole);
	f.main.append(form, box, offer);
	// helpers.js installs this native control's listener during htmx:load, after the swap.
	f.document.addEventListener('htmx:load', () => {
		widen.addEventListener('click', () => { everything.value = '1'; whole.hidden = false; });
	});
	f.document.dispatchEvent(new FixtureEvent('htmx:afterSwap', {detail: {xhr}}));
	assert.equal(everything.value, '', 'do not silently set whole-list scope before controls are enhanced');
	assert.equal(whole.hidden, true);
	f.document.dispatchEvent(new FixtureEvent('htmx:load', {detail: {elt: f.main}}));
	if (finishesFirst) {
		f.document.dispatchEvent(new FixtureEvent('htmx:afterRequest', {detail: {xhr, successful: true}}));
		assert.equal(isolated.isConnected, true, 'a pending settle keeps its watcher alive');
	}
	f.document.dispatchEvent(new FixtureEvent('htmx:afterSettle', {detail: {xhr}}));
	assert.equal(box.checked, true);
	assert.equal(everything.value, '1');
	assert.equal(whole.hidden, false, 'visible selection scope agrees with submitted scope');
	assert.equal(widen.clicks, 1);
	if (!finishesFirst) {
		assert.equal(isolated.isConnected, true, 'the request still owns its form until completion');
		f.document.dispatchEvent(new FixtureEvent('htmx:afterRequest', {detail: {xhr, successful: true}}));
	}
	assert.equal(isolated.isConnected, false);
});

test('restoration uses the latest selection immediately before the matching swap', () => {
	const f = bulkFixture();
	f.swipe(140);
	const isolated = f.document.submissions[0].form;
	const xhr = {};
	isolated.dispatchEvent(new FixtureEvent('htmx:beforeRequest', {detail: {xhr}}));
	// The reader changes their selection while the server is working.
	f.other.checked = false;
	const latest = new Element('input', {type: 'checkbox', name: 'uids', value: '33', form: 'messages-form'});
	latest.form = f.form;
	latest.checked = true;
	f.main.append(latest);
	f.document.dispatchEvent(new FixtureEvent('htmx:beforeSwap', {detail: {xhr}}));
	f.other.checked = false;
	latest.checked = false;
	f.document.dispatchEvent(new FixtureEvent('htmx:afterSwap', {detail: {xhr}}));
	f.document.dispatchEvent(new FixtureEvent('htmx:load', {detail: {elt: f.main}}));
	f.document.dispatchEvent(new FixtureEvent('htmx:afterSettle', {detail: {xhr}}));
	assert.equal(f.other.checked, false, 'an earlier checked row stays deselected');
	assert.equal(latest.checked, true, 'a newly checked row remains selected');
	f.document.dispatchEvent(new FixtureEvent('htmx:afterRequest', {detail: {xhr}}));
});

test('text selection and horizontal scrolling regions keep their native behavior', () => {
	const f = rowFixture();
	const archive = f.action('archive');
	f.window.getSelection = () => ({isCollapsed: false});
	f.swipe(140);
	assert.equal(archive.clicks, 0);
	f.window.getSelection = () => ({isCollapsed: true});
	const code = new Element('pre');
	f.row.append(code);
	f.swipe(140, 0, code);
	assert.equal(archive.clicks, 0);
	const scroll = f.link.parentElement;
	scroll.style.overflowX = 'auto';
	scroll.scrollWidth = 600;
	scroll.clientWidth = 300;
	f.swipe(140);
	assert.equal(archive.clicks, 0);
});

test('permanent delete controls without the semantic gesture hook never run', () => {
	const f = rowFixture();
	f.action('archive');
	const permanent = new Element('button', {'data-key': 'd', type: 'submit', formaction: '/delete-permanently'});
	f.main.append(permanent);
	f.swipe(-140);
	assert.equal(permanent.clicks, 0);
	assert.equal(f.document.submissions.length, 0);
});

test('local settings switches persist and reset changes only the synced mapping fields', () => {
	const f = rowFixture();
	const archive = f.action('archive');
	const toggle = new Element('input', {'data-gesture-toggle': 'rows', type: 'checkbox'});
	f.document.body.append(toggle);
	toggle.checked = false;
	toggle.dispatchEvent(new FixtureEvent('change', {bubbles: true}));
	assert.equal(f.localStorage.getItem('alborz.gestures.rows'), 'off');
	f.swipe(140);
	assert.equal(archive.clicks, 0);
	const select = new Element('select', {name: 'swipe_mail_left', value: 'junk'});
	const unrelated = new Element('select', {name: 'theme', value: 'ink'});
	const reset = new Element('button', {'data-gesture-reset': ''});
	f.document.body.append(select, unrelated, reset);
	reset.click();
	assert.equal(select.value, '');
	assert.equal(unrelated.value, 'ink');
	assert.equal(f.localStorage.getItem('alborz.gestures.rows'), 'off');
});
