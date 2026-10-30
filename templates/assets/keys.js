// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

(() => {
	// A text field owns its letters; a checkbox keeps the focus but takes none.
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

	// A list action is disabled with nothing ticked, so the cursor's row is ticked first.
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

	// Select all on a message takes its body; the frame's document is its own (allow-same-origin).
	const selectBody = () => {
		const frame = document.getElementById("email-frame");
		const node = frame ? frame.contentDocument?.body : document.querySelector("main.message pre");
		if (node) {
			frame?.focus();
			node.ownerDocument.getSelection().selectAllChildren(node);
		}
		return !!node;
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
		"/": () => { const open = document.querySelector(".nav-search-open"); if (open) { open.click(); } },
		"?": () => help(),
	};

	const help = () => {
		const dialog = document.getElementById("keys-help");
		if (dialog && !dialog.open) {
			dialog.showModal();
		}
	};

	// beforetoggle does not bubble, so capture catches it for any opener, swapped-in headers included.
	document.addEventListener("beforetoggle", event => {
		const dialog = event.target;
		if (dialog.id !== "keys-help" || event.newState !== "open") {
			return;
		}
		// A place this page does not hold has no letter.
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
		// A key pressed while an input method composes is part of a character.
		if (ev.defaultPrevented || ev.isComposing) {
			return;
		}
		// The one chord, as other apps have it: search from anywhere, a field or the search itself included.
		if ((ev.ctrlKey || ev.metaKey) && !ev.altKey && !ev.shiftKey && ev.key.toLowerCase() === "k") {
			ev.preventDefault();
			builtin["/"]();
			return;
		}
		if ((ev.ctrlKey || ev.metaKey) && !ev.altKey && !ev.shiftKey && ev.key.toLowerCase() === "a" && !typing(ev.target) && selectBody()) {
			ev.preventDefault();
			return;
		}
		if (ev.ctrlKey || ev.metaKey || ev.altKey || typing(ev.target)) {
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
			// A rail lists the place once per account; the one in this page's scope is meant.
			const places = `[data-go="${CSS.escape(key)}"]`;
			const place = document.querySelector(places + "[data-go-here]") || document.querySelector(places);
			if (place) {
				ev.preventDefault();
				place.click();
			}
			return;
		}
		if (key === "g") {
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
