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

	// A swipe presses the toolbar's own control, so it gets its notice and Undo.
	// Rows laid out as grid cells (display: contents) cannot move.
	const SWIPE_START = 12;
	const SWIPE_ACT = 1 / 3;
	let swipe = null;
	const hintFor = (row, control, side) => {
		const hint = document.createElement("div");
		hint.className = "swipe-hint swipe-" + side;
		hint.textContent = control.getAttribute("aria-label") || control.getAttribute("title") || "";
		hint.style.top = row.offsetTop + "px";
		hint.style.height = row.offsetHeight + "px";
		row.parentElement.append(hint);
		return hint;
	};
	const endSwipe = act => {
		const { row, hint, control } = swipe;
		swipe = null;
		row.classList.remove("swiping");
		row.style.transform = "";
		if (hint) {
			hint.remove();
		}
		if (!act || !control) {
			return;
		}
		for (const box of document.querySelectorAll('input[type="checkbox"][form]:checked')) {
			box.click();
		}
		const box = row.querySelector('input[type="checkbox"][form]');
		if (box) {
			box.click();
			control.click();
		}
	};
	document.addEventListener("pointerdown", ev => {
		// A second finger is a pinch or a scroll, never a swipe.
		if (swipe) {
			if (swipe.row) {
				endSwipe(false);
			}
			swipe = { id: null };
			return;
		}
		const row = ev.pointerType === "touch" && rowOf(ev.target);
		if (!row || !row.classList.contains("message-row") || getComputedStyle(row).display === "contents") {
			return;
		}
		swipe = { id: ev.pointerId, row, x: ev.clientX, y: ev.clientY, dx: 0, hint: null, control: null, live: false };
	});
	document.addEventListener("pointermove", ev => {
		if (!swipe || swipe.id !== ev.pointerId) {
			return;
		}
		const dx = ev.clientX - swipe.x;
		const dy = ev.clientY - swipe.y;
		if (!swipe.live) {
			if (Math.abs(dy) > Math.abs(dx)) {
				swipe = null;
				return;
			}
			if (Math.abs(dx) < SWIPE_START) {
				return;
			}
			swipe.live = true;
			swipe.row.classList.add("swiping");
		}
		const away = (dx < 0) !== (document.documentElement.dir === "rtl");
		const key = away ? "#" : "!";
		if (!swipe.hint || swipe.key !== key) {
			if (swipe.hint) {
				swipe.hint.remove();
			}
			swipe.key = key;
			swipe.control = document.querySelector(`[data-key="${CSS.escape(key)}"]`);
			swipe.hint = swipe.control ? hintFor(swipe.row, swipe.control, away ? "away" : "toward") : null;
		}
		swipe.dx = swipe.control ? dx : 0;
		swipe.row.style.transform = `translateX(${swipe.dx}px)`;
	});
	for (const name of ["pointerup", "pointercancel"]) {
		document.addEventListener(name, ev => {
			if (!swipe) {
				return;
			}
			if (swipe.id === null) {
				swipe = null;
				return;
			}
			if (swipe.id !== ev.pointerId) {
				return;
			}
			const far = swipe.live && name === "pointerup" && Math.abs(swipe.dx) > swipe.row.offsetWidth * SWIPE_ACT;
			const { row, live } = swipe;
			endSwipe(far);
			if (live) {
				// The finger lifting over the row's link must not open it.
				const spent = new AbortController();
				row.addEventListener("click", click => click.preventDefault(), { capture: true, signal: spent.signal });
				setTimeout(() => spent.abort(), 400);
			}
		});
	}

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
