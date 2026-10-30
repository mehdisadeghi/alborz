// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

(() => {
	const START = 12, EDGE = 24, PAGE = 64;
	const still = matchMedia("(prefers-reduced-motion: reduce)");
	const pointers = new Set();
	const local = { rows: true, navigation: true };
	let gesture = null, revealed = null, spent = null;
	const boxes = () => [...document.querySelectorAll('input[type="checkbox"][form]')];
	const keyOf = box => [box.getAttribute("form"), box.name, box.value].join("\n");
	const selection = () => ({
		checked: new Set(boxes().filter(box => box.checked).map(keyOf)),
		all: new Set([...document.querySelectorAll('input[name="everything"]')]
			.filter(input => input.value === "1" && input.form).map(input => input.form.id)),
	});
	const restoreSelection = saved => {
		for (const box of boxes()) {
			if (!box.disabled && (saved.checked.has(keyOf(box)) || saved.all.has(box.getAttribute("form"))) && !box.checked) {
				box.checked = true;
				box.dispatchEvent(new Event("change", { bubbles: true }));
			}
		}
		for (const id of saved.all) {
			const form = document.getElementById(id);
			if (!form) continue;
			let input = [...document.querySelectorAll('input[name="everything"]')]
				.find(input => input.form === form);
			if (!input) {
				input = document.createElement("input");
				input.type = "hidden";
				input.name = "everything";
				input.setAttribute("form", id);
				form.append(input);
			}
			input.value = "1";
			document.querySelector(`.select-everything[data-form="${CSS.escape(id)}"] .select-widen`)?.click();
		}
		const all = document.getElementById("action-checkbox-all");
		if (all) {
			const rows = boxes();
			all.checked = rows.length > 0 && rows.every(box => box.checked);
			all.indeterminate = !all.checked && rows.some(box => box.checked);
		}
	};
	const sync = () => {
		for (const kind of Object.keys(local)) {
			try {
				local[kind] = localStorage.getItem("alborz.gestures." + kind) !== "off";
			} catch {
				// Storage can be denied; the switch still applies in this document.
			}
			document.documentElement.dataset[kind === "rows" ? "rowSwipes" : "swipeNavigation"] = local[kind] ? "on" : "off";
		}
		for (const input of document.querySelectorAll("[data-gesture-toggle]")) {
			input.checked = local[input.dataset.gestureToggle];
			input.disabled = false;
		}
	};
	document.addEventListener("change", event => {
		const kind = event.target.dataset.gestureToggle;
		if (!(kind in local)) return;
		local[kind] = event.target.checked;
		try {
			localStorage.setItem("alborz.gestures." + kind, local[kind] ? "on" : "off");
		} catch {
			// A browser-local preference is optional when storage is unavailable.
		}
		cancel();
		closeReveal();
		sync();
	});
	document.addEventListener("click", event => {
		if (!event.target.closest("[data-gesture-reset]")) return;
		for (const select of document.querySelectorAll('select[name^="swipe_"]')) select.value = "";
	});
	const preferences = () => {
		const node = document.getElementById("swipe-preferences");
		return node ? JSON.parse(node.dataset.swipes) : {};
	};
	const unavailable = control => !control || control.getAttribute("aria-disabled") === "true" ||
		control.classList.contains("is-disabled");
	const controlFor = (row, action) => {
		if (!action || action === "none") return null;
		const selector = `[data-swipe-action="${CSS.escape(action)}"]`;
		const own = row.querySelector(selector);
		if (own) return !unavailable(own) && !own.disabled ? own : null;
		const box = row.querySelector('input[type="checkbox"][form]');
		if (!box || box.disabled || !box.form) return null;
		// Bulk buttons are disabled until a row is selected. Their form identifies
		// the capability; swiping supplies an isolated selection without ticking it.
		return [...document.querySelectorAll(selector)].find(control => control.form === box.form &&
			!unavailable(control) && control.getAttribute("formaction")) || null;
	};
	const clear = state => {
		state.row?.classList.remove("swiping");
		if (state.row) state.row.style.transform = "";
		state.control?.classList.remove("swipe-target", "swipe-armed");
		state.hint?.remove();
	};
	const closeReveal = () => {
		if (!revealed) return;
		clear(revealed);
		revealed = null;
	};
	const cancel = () => {
		if (gesture?.row) clear(gesture);
		gesture = null;
	};
	// Restore the user's other selections if the existing handler redraws a list.
	const watchRequest = (form, saved, cleanup = () => {}) => {
		form.addEventListener("htmx:beforeRequest", event => {
			const xhr = event.detail.xhr;
			let swapped = false, settled = false, finished = false;
			const dispose = () => {
				document.removeEventListener("htmx:beforeSwap", beforeSwap);
				document.removeEventListener("htmx:afterSwap", afterSwap);
				document.removeEventListener("htmx:afterSettle", afterSettle);
				document.removeEventListener("htmx:afterRequest", afterRequest);
				cleanup();
			};
			const beforeSwap = event => { if (event.detail.xhr === xhr) saved = selection(); };
			const afterSwap = event => { if (event.detail.xhr === xhr) swapped = true; };
			const afterSettle = event => {
				if (event.detail.xhr !== xhr) return;
				// helpers.js enhances the new list during htmx:load, before afterSettle.
				// Only then can restoring whole-list scope update its visible indicator.
				restoreSelection(saved);
				settled = true;
				if (finished) dispose();
			};
			const afterRequest = event => {
				if (event.detail.xhr !== xhr) return;
				finished = true;
				if (!swapped || settled || event.detail.successful === false) dispose();
			};
			document.addEventListener("htmx:beforeSwap", beforeSwap);
			document.addEventListener("htmx:afterSwap", afterSwap);
			document.addEventListener("htmx:afterSettle", afterSettle);
			document.addEventListener("htmx:afterRequest", afterRequest);
		}, { once: true });
	};
	const act = (row, control) => {
		if (!row.isConnected || unavailable(control)) return;
		const saved = selection();
		if (row.contains(control)) {
			if (control.disabled) return;
			if (control.form) watchRequest(control.form, saved);
			control.click();
			return;
		}
		const box = row.querySelector('input[type="checkbox"][form]');
		if (!box?.form) return;
		const form = box.form.cloneNode(false);
		form.removeAttribute("id");
		form.hidden = true;
		form.method = "post";
		form.action = control.getAttribute("formaction");
		form.setAttribute("hx-post", form.action);
		form.setAttribute("hx-target", "#main");
		form.setAttribute("hx-swap", "innerHTML show:none");
		const values = new FormData(box.form);
		// Mail's whole-list selection must never widen a one-row gesture.
		for (const name of ["uids", "refs", "paths", "everything"]) values.delete(name);
		values.append(box.name, box.value);
		if (control.name) values.append(control.name, control.value);
		for (const [name, value] of values) {
			const input = document.createElement("input");
			input.type = "hidden";
			input.name = name;
			input.value = value;
			form.append(input);
		}
		document.body.append(form);
		watchRequest(form, saved, () => form.remove());
		if (window.htmx) htmx.process(form);
		form.requestSubmit();
	};
	const hintFor = state => {
		const { row, control, dx } = state;
		const hint = document.createElement("div");
		hint.className = "swipe-hint swipe-" + (dx < 0 ? "left" : "right");
		hint.dataset.action = control.dataset.swipeAction;
		hint.setAttribute("aria-hidden", "true");
		const label = document.createElement("span");
		label.dir = "auto";
		label.textContent = control.getAttribute("aria-label") || control.getAttribute("title") || control.textContent.trim();
		const icon = control.querySelector("svg");
		if (icon) hint.append(icon.cloneNode(true));
		hint.append(label);
		hint.style.top = row.offsetTop + "px";
		hint.style.left = row.offsetLeft + "px";
		hint.style.width = row.offsetWidth + "px";
		hint.style.height = row.offsetHeight + "px";
		row.parentElement.append(hint);
		return hint;
	};
	const reveal = state => {
		revealed = state;
		state.row.style.transform = `translateX(${Math.sign(state.dx) * Math.min(130, state.row.offsetWidth * 0.45)}px)`;
		state.hint.removeAttribute("aria-hidden");
		state.hint.classList.add("revealed");
		const button = document.createElement("button");
		button.type = "button";
		button.className = "swipe-confirm button-danger";
		button.dir = "auto";
		button.textContent = state.control.getAttribute("aria-label") || state.control.textContent.trim();
		button.addEventListener("click", () => {
			closeReveal();
			act(state.row, state.control);
		});
		state.hint.replaceChildren(button);
	};
	const pageLink = (nav, dx) => {
		const next = (dx < 0) !== (document.documentElement.dir === "rtl");
		const control = nav.querySelector(`[data-swipe-step="${next ? "next" : "prev"}"]`);
		return !unavailable(control) && control.matches("a[href]") ? control : null;
	};
	const scrollsHorizontally = (target, region) => {
		for (let el = target; el; el = el.parentElement) {
			if (el.scrollWidth > el.clientWidth && /auto|scroll/.test(getComputedStyle(el).overflowX)) return true;
			if (el === region) break;
		}
		return false;
	};
	document.addEventListener("pointerdown", event => {
		if (event.pointerType !== "touch") return;
		pointers.add(event.pointerId);
		if (pointers.size !== 1 || event.isPrimary === false) { cancel(); return; }
		if (event.target.closest(".swipe-confirm")) return;
		closeReveal();
		const target = event.target;
		if (event.clientX < EDGE || event.clientX > window.innerWidth - EDGE ||
			target.closest('input, button, select, textarea, label, [contenteditable], iframe, pre, code, .flag-split, .attach-chip, [data-no-swipe]') ||
			window.getSelection()?.isCollapsed === false || document.querySelector("dialog[open], [popover]:popover-open")) return;
		let row = local.rows ? target.closest("[data-swipe-kind]") : null;
		const pair = row && preferences()[row.dataset.swipeKind];
		if (row && (!pair || getComputedStyle(row).display === "contents" ||
			(!controlFor(row, pair.left) && !controlFor(row, pair.right)))) row = null;
		let nav = local.navigation ? target.closest("[data-swipe-nav]") : null;
		if (!row && !nav && local.navigation && target.closest("[data-swipe-calendar]")) {
			nav = target.closest("main")?.querySelector("[data-swipe-nav]");
		}
		const region = row || nav;
		if (!region || scrollsHorizontally(target, region)) return;
		const link = target.closest("a");
		if (row && link && !link.closest(".list-primary")) return;
		if (!row && link && !nav.contains(link) && !link.classList.contains("date-link") && !link.closest(".list-primary")) return;
		gesture = { id: event.pointerId, row, nav, region, pair, x: event.clientX, y: event.clientY, dx: 0, live: false, control: null, hint: null };
	});
	document.addEventListener("pointermove", event => {
		const state = gesture;
		if (!state || state.id !== event.pointerId || pointers.size !== 1) return;
		const dx = event.clientX - state.x, dy = event.clientY - state.y;
		if (!state.live) {
			if (Math.max(Math.abs(dx), Math.abs(dy)) < START) return;
			if (Math.abs(dx) < Math.abs(dy) * 1.5) { cancel(); return; }
			state.live = true;
		}
		state.dx = dx;
		if (!state.row) {
			state.control?.classList.remove("swipe-target", "swipe-armed");
			state.control = pageLink(state.nav, dx);
			state.control?.classList.add("swipe-target");
			state.control?.classList.toggle("swipe-armed", Math.abs(dx) >= PAGE);
			return;
		}
		const control = controlFor(state.row, dx < 0 ? state.pair.left : state.pair.right);
		const side = dx < 0 ? "left" : "right";
		if (state.control !== control || state.side !== side) {
			state.hint?.remove();
			state.control = control;
			state.side = side;
			state.hint = control ? hintFor(state) : null;
		}
		state.row.classList.add("swiping");
		state.row.style.transform = `translateX(${control ? dx : 0}px)`;
		state.hint?.classList.toggle("armed", Math.abs(dx) >= Math.max(64, Math.min(120, state.row.offsetWidth / 3)));
	});
	for (const name of ["pointerup", "pointercancel"]) document.addEventListener(name, event => {
		if (event.pointerType !== "touch") return;
		pointers.delete(event.pointerId);
		const state = gesture;
		if (!state || state.id !== event.pointerId) return;
		gesture = null;
		if (!state.live) { clear(state); return; }
		spent = { id: event.pointerId, region: state.region, until: performance.now() + 400 };
		const threshold = state.row ? Math.max(64, Math.min(120, state.row.offsetWidth / 3)) : PAGE;
		if (name === "pointercancel" || Math.abs(state.dx) < threshold) {
			if (state.row) {
				state.row.animate([{ transform: state.row.style.transform }, { transform: "none" }], { duration: still.matches ? 0 : 150, easing: "ease-out" });
			}
			clear(state);
			return;
		}
		if (!state.row) {
			const link = pageLink(state.nav, state.dx);
			clear(state);
			link?.click();
		} else if (state.control?.hasAttribute("data-swipe-confirm")) {
			reveal(state);
		} else {
			clear(state);
			if (state.control) act(state.row, state.control);
		}
	});
	document.addEventListener("click", event => {
		if (!spent || event.detail === 0 || event.target.closest(".swipe-confirm")) return;
		if (performance.now() <= spent.until && spent.region.contains(event.target) &&
			(event.pointerId === undefined || event.pointerId === spent.id)) {
			event.preventDefault();
			event.stopImmediatePropagation();
		}
		spent = null;
	}, true);
	for (const name of ["contextmenu", "htmx:beforeSwap"]) document.addEventListener(name, () => { cancel(); closeReveal(); });
	window.addEventListener("blur", () => { pointers.clear(); cancel(); closeReveal(); });
	window.addEventListener("storage", sync);
	document.addEventListener("htmx:load", sync);
	sync();
})();
// @license-end
