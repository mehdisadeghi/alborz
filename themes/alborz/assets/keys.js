// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// The keyboard, in the scheme webmail settled on and Gmail wrote down,
// which is vi's where the two overlap: j and k walk a list, x ticks a
// row, / searches, g and a letter goes somewhere, ? lists them all.
//
// Nothing here knows a page. The markup says what a key does - an
// element carries data-key, a place carries data-go - and a key presses
// the element, so an action has one implementation, the button's, and a
// page gains a shortcut by naming it. The cursor is the browser's own
// focus, on the row's link: Enter opens it and the focus ring shows it
// with no state kept here, and a reader who never touches these keys
// meets none of it.

(() => {
	// A field that takes text owns its letters. A ticked checkbox keeps
	// the focus and takes none, so the keys go on working under it.
	const untyped = /^(checkbox|radio|button|submit|reset|image|file|range|color)$/;
	const typing = el => el && (el.isContentEditable || /^(TEXTAREA|SELECT)$/.test(el.tagName) ||
		(el.tagName === "INPUT" && !untyped.test(el.type)));
	const dataRows = () => [...document.querySelectorAll(".list-row")].filter(row => row.querySelector(".list-cell"));
	const rowOf = el => el && el.closest ? el.closest(".list-row") : null;
	const linkOf = row => row.querySelector(".list-primary a") || row.querySelector("a");

	const walk = step => {
		const rows = dataRows();
		if (rows.length === 0) {
			return;
		}
		const at = rows.indexOf(rowOf(document.activeElement));
		const to = at < 0 ? (step > 0 ? 0 : rows.length - 1) : Math.min(rows.length - 1, Math.max(0, at + step));
		linkOf(rows[to]).focus();
	};

	// A list's action works on the ticked rows and is disabled with none.
	// With none ticked the key means the row under the cursor, so it is
	// ticked first - which is what a hand would do.
	const press = el => {
		const row = rowOf(document.activeElement);
		if (el.disabled && row) {
			const box = row.querySelector('input[type="checkbox"][form]');
			if (box && !box.checked) {
				box.click();
			}
		}
		if (!el.disabled) {
			el.click();
		}
	};

	const inRow = selector => {
		const row = rowOf(document.activeElement);
		return row && row.querySelector(selector);
	};

	const builtin = {
		j: () => walk(1),
		k: () => walk(-1),
		o: () => { const row = rowOf(document.activeElement); if (row) { linkOf(row).click(); } },
		x: () => { const box = inRow('input[type="checkbox"][form]'); if (box) { box.click(); } },
		s: () => { const star = inRow(".flag-button") || document.querySelector(".subject-row .flag-button"); if (star) { star.click(); } },
		"/": () => { const search = document.querySelector(".actions-search input"); if (search) { search.focus(); search.select(); } },
		"?": () => help(),
	};

	const help = () => {
		const dialog = document.getElementById("keys-help");
		if (dialog && !dialog.open) {
			dialog.showModal();
		}
	};

	// The dialog says what this page's own controls answer to, in their
	// own words, and it reads them as it opens, whoever opens it: the
	// key here, or the menu's button, which the browser answers without
	// a script. The event does not bubble, so it is caught on the way
	// down, and a swapped-in header needs no listener of its own.
	document.addEventListener("beforetoggle", event => {
		const dialog = event.target;
		if (dialog.id !== "keys-help" || event.newState !== "open") {
			return;
		}
		// A place this page does not hold - a section with no plugin
		// behind it, the starred view outside mail - has no letter.
		for (const kbd of dialog.querySelectorAll("[data-go-key]")) {
			kbd.hidden = !document.querySelector(`[data-go="${CSS.escape(kbd.dataset.goKey)}"]`);
		}
		const page = dialog.querySelector(".keys-page");
		page.replaceChildren();
		const seen = new Set();
		for (const el of document.querySelectorAll("[data-key]")) {
			const key = el.dataset.key;
			const label = el.getAttribute("aria-label") || el.getAttribute("title") || el.textContent.trim();
			if (seen.has(key) || !label) {
				continue;
			}
			seen.add(key);
			const dt = document.createElement("dt");
			const kbd = document.createElement("kbd");
			kbd.textContent = key;
			dt.append(kbd);
			const dd = document.createElement("dd");
			dd.textContent = label;
			page.append(dt, dd);
		}
	}, true);

	let going = 0;
	document.addEventListener("keydown", ev => {
		// A key pressed while an input method composes is part of a
		// character, not a command.
		if (ev.defaultPrevented || ev.isComposing || ev.ctrlKey || ev.metaKey || ev.altKey || typing(ev.target)) {
			return;
		}
		const dialog = document.querySelector("dialog[open], [popover]:popover-open");
		if (dialog) {
			return;
		}
		const key = ev.key;
		if (going) {
			clearTimeout(going);
			going = 0;
			// A rail lists the place once per account; the one in the
			// page's own scope is the one meant.
			const places = `[data-go="${CSS.escape(key)}"]`;
			const place = document.querySelector(places + "[data-go-here]") || document.querySelector(places);
			if (place) {
				ev.preventDefault();
				place.click();
			}
			return;
		}
		if (key === "g") {
			// The second letter is awaited for as long as a hand takes.
			going = setTimeout(() => { going = 0; }, 1500);
			return;
		}
		const el = document.querySelector(`[data-key="${CSS.escape(key)}"]`);
		if (el) {
			ev.preventDefault();
			press(el);
		} else if (builtin[key]) {
			ev.preventDefault();
			builtin[key]();
		}
	});
})();
