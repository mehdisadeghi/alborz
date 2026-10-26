// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// A refinement stays in its list's history entry; opening an object takes a step.
// Browser Back remains native. Only an explicit link to a parent we recorded can go Back.
(() => {
	const lists = new Map([
		['/mail/search', 'mail'], ['/calendar', 'calendar'], ['/calendar/date', 'calendar'],
		['/calendar/search', 'calendar'], ['/tasks', 'tasks'], ['/journal', 'journal'],
		['/contacts', 'contacts'], ['/search', 'search'],
	]);
	const reserved = new Set(['create', 'copy', 'move', 'import', 'export', 'export-all', 'refresh', 'delete', 'complete', 'quick-preview']);
	const page = url => {
		const path = url.pathname;
		const list = lists.get(path) || (/^\/mailbox\/[^/]+$/.test(path) && 'mail');
		if (list) return {section: list, kind: 'list'};
		if (/^\/message\/[^/]+\/\d+$/.test(path)) return {section: 'mail', kind: 'object', object: path};
		const dav = /^\/(calendar|tasks|journal|contacts)\/([^/]+)(\/(?:edit|update))?$/.exec(path);
		if (dav && !reserved.has(dav[2])) {
			return {section: dav[1], kind: dav[3] ? 'editor' : 'object', object: `/${dav[1]}/${dav[2]}`};
		}
		return {};
	};
	let parent, savedEntry;
	// htmx replaces history.state when saving a page, including during Back/Forward.
	document.addEventListener('htmx:beforeHistorySave', () => {
		savedEntry = {href: location.href, parent: history.state?.alborzParent};
	});
	const restoreParent = () => {
		if (savedEntry?.href === location.href) {
			history.replaceState({...history.state, alborzParent: savedEntry.parent}, '');
		}
		savedEntry = null;
	};
	for (const event of ['htmx:historyCacheMiss', 'htmx:historyCacheHit']) {
		document.addEventListener(event, restoreParent);
	}
	document.addEventListener('htmx:beforeHistoryUpdate', ev => {
		restoreParent();
		const change = ev.detail.history;
		const from = new URL(location.href);
		const to = new URL(change.path, from);
		const source = page(from), destination = page(to);
		const request = ev.detail.requestConfig;
		if (change.type === 'push' && from.origin === to.origin && request) {
			const sameSection = source.section && source.section === destination.section;
			const sameObject = sameSection && source.object && source.object === destination.object &&
				from.searchParams.get('account') === to.searchParams.get('account');
			const listsOnly = sameSection && source.kind === 'list' && destination.kind === 'list';
			const neighbours = sameSection && source.kind === 'object' && destination.kind === 'object' &&
				request.elt?.matches('a[data-swipe-step]');
			const objectView = sameObject && source.kind === 'object' && destination.kind === 'object';
			const edit = sameObject && (source.kind === 'editor' || destination.kind === 'editor');
			if ((request.verb === 'get' && (listsOnly || neighbours || objectView || edit)) ||
				(request.verb === 'post' && edit)) change.type = 'replace';
		}
		parent = change.type === 'push' ? from.href : history.state?.alborzParent;
	});
	for (const event of ['htmx:pushedIntoHistory', 'htmx:replacedInHistory']) {
		document.addEventListener(event, () => {
			history.replaceState({...history.state, alborzParent: parent}, '');
		});
	}
	document.addEventListener('click', ev => {
		if (ev.defaultPrevented || ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
		const link = ev.target.closest('a[href]');
		if (!link || link.hasAttribute('download') || (link.target && link.target !== '_self')) return;
		const here = page(new URL(location.href));
		if (here.kind !== 'object' && here.kind !== 'editor') return;
		if (link.href !== history.state?.alborzParent) return;
		ev.preventDefault();
		ev.stopImmediatePropagation();
		history.back();
	}, true);
})();
