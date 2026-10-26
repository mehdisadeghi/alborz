const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, '../templates/assets/history.js'), 'utf8');

function fixture(start) {
	const events = {};
	const entries = [new URL(start, 'https://example.test').href];
	const states = [{}];
	let index = 0;
	const location = {href: entries[0]};
	const save = () => {
		events['htmx:beforeHistorySave']();
		states[index] = {htmx: true};
	};
	const history = {
		get state() { return states[index]; },
		replaceState(state) { states[index] = state; },
		back() {
			if (index > 0) location.href = entries[--index];
			save();
			events['htmx:historyCacheMiss']();
			return location.href;
		},
	};
	vm.runInNewContext(script, {
		URL, location, history,
		document: {addEventListener(name, fn) { events[name] = fn; }},
	});
	return {
		entries,
		navigate(to, {verb = 'get', pager = false, type = 'push'} = {}) {
			const change = {type, path: to};
			save();
			events['htmx:beforeHistoryUpdate']({detail: {
				history: change,
				requestConfig: {verb, elt: {matches: selector => pager && selector === 'a[data-swipe-step]'}},
			}});
			location.href = new URL(to, location.href).href;
			if (change.type === 'replace') entries[index] = location.href;
			else {
				entries.splice(++index, entries.length, location.href);
				states.splice(index, states.length, {});
			}
			events[change.type === 'replace' ? 'htmx:replacedInHistory' : 'htmx:pushedIntoHistory']({detail: {path: to}});
			return change.type;
		},
		back: history.back,
		forward() {
			if (index + 1 < entries.length) location.href = entries[++index];
			save();
			events['htmx:historyCacheMiss']();
			return location.href;
		},
		click(to, extra = {}) {
			let prevented = false;
			const link = {href: new URL(to, location.href).href, target: '', hasAttribute: () => false};
			events.click({button: 0, target: {closest: () => link}, preventDefault() { prevented = true; },
				stopImmediatePropagation() {}, ...extra});
			return {prevented, href: location.href};
		},
		events,
	};
}

test('list refinements and message neighbours leave one Back step to the latest list', () => {
	const f = fixture('/mailbox/INBOX');
	assert.equal(f.navigate('/mailbox/INBOX?view=unread'), 'replace');
	assert.equal(f.navigate('/mailbox/INBOX?view=unread&page=2'), 'replace');
	assert.equal(f.navigate('/message/INBOX/10?from=%2Fmailbox%2FINBOX'), 'push');
	assert.equal(f.navigate('/message/INBOX/11?from=%2Fmailbox%2FINBOX', {pager: true}), 'replace');
	assert.equal(f.back(), 'https://example.test/mailbox/INBOX?view=unread&page=2');
	assert.equal(f.forward(), 'https://example.test/message/INBOX/11?from=%2Fmailbox%2FINBOX');
	assert.equal(f.entries.length, 2);
});

test('section list views, folders, searches, and account choices replace their current list', () => {
	for (const [from, to] of [
		['/mailbox/INBOX', '/mailbox/Archive?account=one'],
		['/mailbox/INBOX', '/mail/search?query=review'],
		['/calendar?month=2026-10', '/calendar/date?date=2026-10-26'],
		['/calendar/date?date=2026-10-26', '/calendar/search?query=review'],
		['/tasks?view=all', '/tasks?view=completed&page=2'],
		['/journal', '/journal?cal=work&query=review'],
		['/contacts', '/contacts?group=team&sort=name'],
		['/search?query=one', '/search?query=two'],
	]) assert.equal(fixture(from).navigate(to), 'replace', `${from} → ${to}`);
});

test('message body and image view changes keep Back pointed at the list', () => {
	const f = fixture('/mailbox/INBOX?view=unread');
	f.navigate('/message/INBOX/1?account=a');
	assert.equal(f.navigate('/message/INBOX/1?account=a&part=1'), 'replace');
	assert.equal(f.navigate('/message/INBOX/1?account=a&part=2&images=1'), 'replace');
	assert.equal(f.back(), 'https://example.test/mailbox/INBOX?view=unread');
	assert.equal(f.entries.length, 2);
	assert.equal(fixture('/message/INBOX/1?account=a').navigate('/message/INBOX/1?account=b'), 'push');
});

test('new sections, objects, editors, and forms remain meaningful history steps', () => {
	for (const [from, to] of [
		['/mailbox/INBOX', '/calendar'],
		['/calendar', '/calendar/create'],
		['/calendar', '/calendar/import'],
		['/calendar', '/calendar/copy'],
		['/tasks', '/tasks/task.ics'],
		['/tasks/parent.ics', '/tasks/child.ics'],
		['/message/INBOX/1', '/message/INBOX/2'],
		['/message/INBOX/1', '/message/INBOX/1/reply'],
		['/settings/profile', '/settings/reading'],
	]) assert.equal(fixture(from).navigate(to), 'push', `${from} → ${to}`);
});

test('DAV neighbour controls replace details while preserving nested object links', () => {
	for (const section of ['calendar', 'tasks', 'journal', 'contacts']) {
		assert.equal(fixture(`/${section}/one`).navigate(`/${section}/two`, {pager: true}), 'replace');
		assert.equal(fixture(`/${section}/one`).navigate(`/${section}/two`), 'push');
		assert.equal(fixture(`/${section}/one`).navigate(`/${section}/create`, {pager: true}), 'push');
	}
});

test('editing and saving share the same owned object history entry', () => {
	for (const [edit, object] of [
		['/calendar/event.ics/update?account=a', '/calendar/event.ics?account=a&from=%2Fcalendar'],
		['/tasks/task.ics/edit', '/tasks/task.ics'],
		['/journal/note.ics/edit', '/journal/note.ics'],
		['/contacts/person.vcf/edit', '/contacts/person.vcf'],
	]) assert.equal(fixture(edit).navigate(object, {verb: 'post'}), 'replace');
	assert.equal(fixture('/tasks/one/edit').navigate('/tasks/two', {verb: 'post'}), 'push');
	assert.equal(fixture('/tasks/one/edit?account=a').navigate('/tasks/one?account=b', {verb: 'post'}), 'push');
	assert.equal(fixture('/tasks/one/edit').navigate('/tasks/one'), 'replace');
	assert.equal(fixture('/tasks/one/edit').navigate('/tasks/one/edit', {verb: 'post'}), 'replace');
	const f = fixture('/tasks?view=all');
	f.navigate('/tasks/one');
	assert.equal(f.navigate('/tasks/one/edit'), 'replace');
	f.navigate('/tasks/one', {verb: 'post'});
	assert.equal(f.back(), 'https://example.test/tasks?view=all');
	assert.equal(f.entries.length, 2);
});

test('explicit Up and Cancel traverse an exact recorded parent without creating duplicate lists', () => {
	for (const edit of [false, true]) {
		const f = fixture('/tasks?view=all');
		f.navigate('/tasks/one');
		if (edit) f.navigate('/tasks/one/edit');
		assert.deepEqual(f.click('/tasks?view=all'), {prevented: true, href: 'https://example.test/tasks?view=all'});
		assert.equal(f.entries.length, 2);
		assert.equal(f.forward(), `https://example.test/tasks/one${edit ? '/edit' : ''}`);
		assert.equal(f.click('/tasks?view=all').prevented, true);
	}
	assert.equal(fixture('/tasks/one?from=%2Ftasks').click('/tasks').prevented, false);
	const f = fixture('/tasks');
	f.navigate('/tasks/one');
	assert.equal(f.click('/tasks', {ctrlKey: true}).prevented, false);
	assert.equal(f.click('/tasks', {defaultPrevented: true}).prevented, false);
	assert.equal(f.click('/tasks?view=all').prevented, false);
});

test('direct entry, external links, actions, and existing replacement rules remain native', () => {
	const f = fixture('/message/INBOX/1');
	assert.equal(f.navigate('https://elsewhere.test/message/INBOX/2', {pager: true}), 'push');
	assert.equal(fixture('/tasks').navigate('/tasks?view=all', {verb: 'post'}), 'push');
	assert.equal(fixture('/tasks').navigate('/calendar', {type: 'replace'}), 'replace');
});
