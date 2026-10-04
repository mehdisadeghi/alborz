// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// The zone the server formats dates in; the page writes it, so it cannot be HttpOnly.
try {
	const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
	if (tz && document.cookie.indexOf("alborz_tz=") === -1) {
		const secure = location.protocol === "https:" ? ";Secure" : "";
		document.cookie =
			"alborz_tz=" + tz + ";path=/;max-age=31536000;SameSite=Lax" + secure;
	}
} catch (e) {}


// On a touch screen a focused field puts a keyboard over half the page.
const touch = window.matchMedia && window.matchMedia("(pointer: coarse)").matches;
if (touch) {
	const opening = document.activeElement;
	if (opening && opening.hasAttribute("autofocus")) {
		opening.blur();
	}
}

// enhance runs again on every htmx swap, and a listener added twice acts twice.
const claimed = new WeakMap();
const once = (el, key) => {
	let keys = claimed.get(el);
	if (!keys) {
		keys = new Set();
		claimed.set(el, keys);
	}
	if (keys.has(key)) {
		return false;
	}
	keys.add(key);
	return true;
};

// One fit per toolbar: a second one, from enhance running again, would take the items the first moved for the menu's own.
const fits = new WeakMap();

const enhance = () => {
	// Set here because the page CSP allows no inline script.
	const tz_input = document.getElementById("timezone");
	if (tz_input && !tz_input.value) {
		try {
			tz_input.placeholder = Intl.DateTimeFormat().resolvedOptions().timeZone;
		} catch (e) {}
	}

	// A list is any form its rows' checkboxes name; its controls are disabled while nothing is ticked.
	const check_all = document.getElementById("action-checkbox-all");
	const bulk_forms = new Set(Array.prototype.map.call(
		document.querySelectorAll('input[type="checkbox"][form]'), box => box.getAttribute("form")));
	for (const formId of bulk_forms) {
		const controls = document.querySelectorAll(
			`button[form="${formId}"], select[form="${formId}"]`,
		);
		if (controls.length === 0) {
			continue;
		}
		const boxes = document.querySelectorAll(`input[type="checkbox"][form="${formId}"]`);
		const menus = document.querySelectorAll(`[data-gated="${formId}"]`);
		// A fully ticked page may widen to the whole list; unticking one narrows it back.
		const offer = document.querySelector(`.select-everything[data-form="${formId}"]`);
		const everything = document.querySelector(`input[name="everything"][form="${formId}"]`);
		const widen = on => {
			everything.value = on ? "1" : "";
			offer.querySelector(".select-page").hidden = on;
			offer.querySelector(".select-widen").hidden = on;
			offer.querySelector(".select-whole").hidden = !on;
			offer.querySelector(".select-narrow").hidden = !on;
		};
		const update = () => {
			const any = Array.prototype.some.call(boxes, box => box.checked);
			if (offer) {
				const page = boxes.length > 0 && Array.prototype.every.call(boxes, box => box.checked);
				offer.hidden = !page;
				if (!page) {
					widen(false);
				}
			}
			for (const control of controls) {
				control.disabled = !any;
			}
			for (const menu of menus) {
				menu.querySelector(":scope > button").disabled = !any;
				const panel = menu.querySelector(":scope > [popover]");
				if (!any && panel.matches(":popover-open")) {
					panel.hidePopover();
				}
			}
		};
		for (const box of boxes) {
			if (once(box, "bulk")) {
				box.addEventListener("change", update);
			}
		}
		if (offer && once(offer, "bulk")) {
			offer.querySelector(".select-widen").addEventListener("click", () => widen(true));
			offer.querySelector(".select-narrow").addEventListener("click", () => {
				for (const box of boxes) {
					box.checked = false;
				}
				if (check_all) {
					check_all.checked = false;
				}
				update();
			});
		}
		if (check_all) {
			// The markup ships it disabled for want of a script; from here only an empty list disables it.
			check_all.title = check_all.dataset.title || "";
			check_all.disabled = boxes.length === 0;
			if (once(check_all, "bulk")) {
				check_all.addEventListener("click", ev => {
					for (const box of boxes) {
						box.checked = ev.target.checked;
					}
					update();
				});
			}
		}
		update();
	}

	for (const search of document.querySelectorAll(".actions-search input")) {
		if (!once(search, "escape")) {
			continue;
		}
		search.addEventListener("keydown", ev => {
			if (ev.key === "Escape") {
				ev.currentTarget.value = "";
				ev.currentTarget.blur();
			}
		});
	}

	// The chosen account narrows the places to its own; without a script the groups say whose is whose.
	for (const account of document.querySelectorAll("select[data-place-account]")) {
		const places = account.form && account.form.querySelector("select[data-place-of]");
		if (!places || !once(account, "placeaccount")) {
			continue;
		}
		const narrow = () => {
			for (const group of places.querySelectorAll("optgroup")) {
				group.hidden = account.value !== "" && group.label !== account.value;
			}
			for (const option of places.querySelectorAll("option[data-account]")) {
				option.hidden = account.value !== "" && option.dataset.account !== account.value;
			}
			const chosen = places.selectedOptions[0];
			if (chosen && chosen.hidden) {
				places.value = "";
			}
		};
		account.addEventListener("change", narrow);
		narrow();
	}

	for (const select of document.querySelectorAll("select[data-destination-key]")) {
		if (!once(select, "destination")) {
			continue;
		}
		const key = "alborz.destination." + select.dataset.destinationKey;
		try {
			const saved = localStorage.getItem(key);
			if (saved && Array.prototype.some.call(select.options, option => option.value === saved)) {
				select.value = saved;
			}
			select.addEventListener("change", () => localStorage.setItem(key, select.value));
			select.form.addEventListener("submit", () => localStorage.setItem(key, select.value));
		} catch (e) {}
	}

	const submit_on_change = document.querySelectorAll("[data-submit-on-change]");
	for (let i = 0; i < submit_on_change.length; i++) {
		if (!once(submit_on_change[i], "submitonchange")) {
			continue;
		}
		submit_on_change[i].addEventListener("change", ev => {
			ev.currentTarget.form.requestSubmit();
		});
	}

	// The reply is written above the quote or below it; data-caret says which end.
	const caret = document.querySelector("textarea[data-caret]");
	if (caret && !touch && once(caret, "caret")) {
		const at = caret.dataset.caret === "start" ? 0 : caret.value.length;
		caret.focus();
		caret.setSelectionRange(at, at);
	}

	// A datalist matches the whole value, so each option carries what is typed before the candidate.
	const recipients = document.querySelectorAll('input[list="emails"]');
	const suggestions = document.getElementById("emails");
	if (suggestions && recipients.length) {
		const all = [...suggestions.options].map(o => o.value);
		for (const field of recipients) {
			if (!once(field, "recipients")) {
				continue;
			}
			field.addEventListener("input", () => {
				const cut = field.value.lastIndexOf(",");
				if (cut < 0) {
					if (suggestions.options.length && suggestions.options[0].value !== all[0]) {
						suggestions.replaceChildren(...all.map(v => new Option(v, v)));
					}
					return;
				}
				const prefix = field.value.slice(0, cut + 1) + " ";
				suggestions.replaceChildren(...all.map(v => {
					const option = new Option(v, prefix + v);
					option.label = v;
					return option;
				}));
			});
		}
	}

	const handler_group = document.getElementById("handler-group");
	if (handler_group && navigator.registerProtocolHandler && once(handler_group, "handlers")) {
		handler_group.hidden = false;
		// No API says whether the browser agreed, so the page remembers only that it asked.
		const asked = [...handler_group.querySelectorAll("button[data-protocol]")]
			.some((b) => localStorage.getItem(b.dataset.protocol + "-asked"));
		if (asked) {
			document.getElementById("register-mailto-before").hidden = false;
		}
		handler_group.addEventListener("click", (event) => {
			const button = event.target.closest("button[data-protocol]");
			if (!button) {
				return;
			}
			try {
				navigator.registerProtocolHandler(button.dataset.protocol, button.dataset.handler);
				localStorage.setItem(button.dataset.protocol + "-asked", "1");
				document.getElementById("register-mailto-before").hidden = true;
				document.getElementById("register-mailto-asked").hidden = false;
			} catch (e) {
				document.getElementById("register-mailto-refused").hidden = false;
			}
		});
	}

	// A form submitted from the phone drawer reopens it on the next page; a rail link's page is the reason for the tap.
	const drawer = document.getElementById("sidebar");
	if (drawer) {
		try {
			if (sessionStorage.getItem("drawer") === "1") {
				drawer.checked = true;
				sessionStorage.removeItem("drawer");
			}
		} catch (e) {}
		for (const form of document.querySelectorAll("aside form")) {
			if (!once(form, "drawer")) {
				continue;
			}
			form.addEventListener("submit", () => {
				try {
					if (drawer.checked) {
						sessionStorage.setItem("drawer", "1");
					}
				} catch (e) {}
			});
		}
	}

	for (const d of document.querySelectorAll("details[data-rail-key]")) {
		if (!once(d, "railkey")) {
			continue;
		}
		const key = "rail:" + d.dataset.railKey;
		try {
			const v = localStorage.getItem(key);
			if (v !== null) {
				d.open = v === "1";
			}
		} catch (e) {}
		d.addEventListener("toggle", () => {
			try {
				localStorage.setItem(key, d.open ? "1" : "0");
			} catch (e) {}
		});
	}

	// Measured, not set at a width: the words' length differs by language.
	const primeNav = document.querySelector("header nav");
	if (primeNav) {
		const fitNav = () => {
			primeNav.classList.remove("is-icons");
			if (primeNav.scrollWidth > primeNav.clientWidth) {
				primeNav.classList.add("is-icons");
			}
		};
		if (once(primeNav, "fitnav")) {
			new ResizeObserver(fitNav).observe(primeNav);
		}
		fitNav();
	}

	// Priority-plus toolbar: data-yield is "hide", "word", "split" (a group whose members move into the menu one by one), or by default a move into the overflow menu.
	for (const wrap of document.querySelectorAll(".actions-wrap")) {
		if (fits.has(wrap)) {
			fits.get(wrap).fit();
			continue;
		}
		const details = wrap.querySelector("div.overflow:not(.flag-menu)");
		const menu = details && details.querySelector(":scope > .overflow-menu");
		if (!menu) {
			continue;
		}
		const own = () => Array.prototype.filter.call(wrap.querySelectorAll("[data-priority]"), el => !menu.contains(el));
		if (own().length === 0) {
			continue;
		}
		const ownItems = menu.children.length;
		// Held at full size while measuring: a flex-shrunk title spills unseen by scrollWidth.
		const pinned = wrap.querySelectorAll(":scope > .actions-message, :scope > .actions-end, .actions-message h1");
		const pin = on => {
			for (const el of pinned) {
				el.style.flexShrink = on ? "0" : "";
				el.style.width = on ? "auto" : "";
			}
		};
		let items = [];
		let parts = [];
		let fitting = false;
		const unfit = () => {
			for (let i = parts.length - 1; i >= 0; i--) {
				parts[i].parent.insertBefore(parts[i].el, parts[i].next);
			}
			parts = [];
			for (let i = items.length - 1; i >= 0; i--) {
				items[i].el.hidden = false;
				items[i].el.classList.remove("no-word");
				items[i].parent.insertBefore(items[i].el, items[i].next);
			}
			items = [];
		};
		const fit = () => {
			if (fitting) {
				return;
			}
			fitting = true;
			unfit();
			// Collected after the restore: what the last run moved is back on the row, and a swap may have brought new items.
			items = own().map(el => ({ el, priority: Number(el.dataset.priority), how: el.dataset.yield, parent: el.parentNode, next: el.nextSibling }));
			const leaving = items.slice().sort((a, b) => a.priority - b.priority);
			details.hidden = ownItems === 0;
			pin(true);
			const moved = new Set();
			for (const it of leaving) {
				if (wrap.scrollWidth <= wrap.clientWidth) {
					break;
				}
				if (it.how === "hide") {
					it.el.hidden = true;
				} else if (it.how === "word") {
					it.el.classList.add("no-word");
				} else {
					details.hidden = false;
					moved.add(it.el);
					it.el.remove();
				}
			}
			pin(false);
			for (let i = items.length - 1; i >= 0; i--) {
				if (!moved.has(items[i].el)) {
					continue;
				}
				if (items[i].how !== "split") {
					menu.prepend(items[i].el);
					continue;
				}
				const members = Array.from(items[i].el.children);
				for (let k = members.length - 1; k >= 0; k--) {
					parts.push({ el: members[k], parent: items[i].el, next: members[k].nextSibling });
					menu.prepend(members[k]);
				}
			}
			fitting = false;
		};
		fits.set(wrap, { fit, unfit });
		new ResizeObserver(fit).observe(wrap);
		// Opening the search widens it without resizing the row, which the observer alone would miss.
		for (const type of ["focusin", "focusout"]) {
			wrap.addEventListener(type, ev => {
				if (ev.target.closest(".actions-search")) {
					requestAnimationFrame(fit);
				}
			});
		}
		fit();
	}


	// Without anchor positioning the UA sheet centres a popover in the viewport.
	if (!CSS.supports("position-area", "block-end")) {
		for (const panel of document.querySelectorAll("[popover].overflow-menu, [popover].accounts-menu")) {
			if (!once(panel, "popoverplace")) {
				continue;
			}
			panel.addEventListener("toggle", event => {
				if (event.newState !== "open") {
					return;
				}
				const button = document.querySelector(`[popovertarget="${panel.id}"]`);
				const box = button.getBoundingClientRect();
				panel.style.top = `${box.bottom + 2}px`;
				if (document.documentElement.dir === "rtl") {
					panel.style.left = `${box.left}px`;
				} else {
					panel.style.right = `${window.innerWidth - box.right}px`;
				}
			});
		}
	}

};

enhance();
// A quick add lands back at #quick-add so the next line can be typed straight away.
// The hash names no element: one that did would scroll the page to it.
if (location.hash === "#quick-add") {
	document.getElementById("quick")?.focus();
}
document.addEventListener("htmx:load", enhance);

// htmx lets a removed element's request run on; aborting it lets the server stop, as for a signature check.
document.addEventListener("htmx:beforeCleanupElement", ev => {
	if (ev.target.classList && ev.target.classList.contains("htmx-request")) {
		htmx.trigger(ev.target, "htmx:abort");
	}
});

// A suggestion popup open when htmx swaps the body would outlive its field; focus outside the swap stays.
document.addEventListener("htmx:beforeSwap", ev => {
	const focused = document.activeElement;
	if (focused && focused !== document.body && focused.blur && ev.detail.target.contains(focused)) {
		focused.blur();
	}
});

// A refusal is an answer, not a failure: swap it and raise no notice.
document.addEventListener("htmx:beforeSwap", ev => {
	const status = ev.detail && ev.detail.xhr && ev.detail.xhr.status;
	if (status === 401 || status === 409 || status === 422) {
		ev.detail.shouldSwap = true;
		ev.detail.isError = false;
	}
});

// An error page alborz wrote is shown; the notice is for no answer.
document.addEventListener("htmx:beforeSwap", ev => {
	const xhr = ev.detail && ev.detail.xhr;
	if (!xhr || xhr.status < 500 || !ev.detail.boosted) {
		return;
	}
	if (/<meta name="alborz-shell"/.test(xhr.responseText)) {
		ev.detail.shouldSwap = true;
		ev.detail.isError = false;
	}
});

// A swap replaces the body only, so lang and dir on the root come from the answer.
document.addEventListener("htmx:beforeSwap", ev => {
	const answer = ev.detail && ev.detail.xhr && ev.detail.xhr.responseText;
	if (!answer) {
		return;
	}
	const root = /<html[^>]*\slang="([A-Za-z-]+)"[^>]*\sdir="(ltr|rtl)"/i.exec(answer);
	if (root) {
		document.documentElement.lang = root[1];
		document.documentElement.dir = root[2];
	}
});

// An open page keeps its first head across deploys, so an answer built for another shell loads whole (ADR 19).
// Every answer names its shell in a header, pieces and history restores alike.
const loadedShell = (document.querySelector('meta[name="alborz-shell"]') || {}).content;
const otherShell = xhr => {
	const named = xhr.getResponseHeader("Alborz-Shell");
	return Boolean(loadedShell && named && named !== loadedShell);
};
// Only for a GET: a refused form carries what was typed.
document.addEventListener("htmx:beforeSwap", ev => {
	const xhr = ev.detail && ev.detail.xhr;
	const config = ev.detail && ev.detail.requestConfig;
	if (xhr && config && config.verb === "get" && otherShell(xhr)) {
		ev.detail.shouldSwap = false;
		location.assign(xhr.responseURL);
	}
});
// htmx swaps a restore without asking, so it lands in a detached element while the page reloads.
document.addEventListener("htmx:historyCacheMissLoad", ev => {
	if (otherShell(ev.detail.xhr)) {
		ev.detail.historyElt = document.createElement("body");
		location.reload();
	}
});

const unasked = "Alborz-Unasked";
// Looked up per event: a boosted navigation replaces the body.
const failedNotice = () => document.getElementById("request-failed");
// Offline already says the server did not answer.
const offlineShown = () => {
	const offline = document.getElementById("offline-notice");
	return offline !== null && offline.open;
};
// htmx swaps nothing on a failure, so without the notice a click would look ignored.
for (const event of ["htmx:sendError", "htmx:timeout", "htmx:responseError"]) {
	document.addEventListener(event, ev => {
		// What the page asked for by itself is not a click that did nothing.
		if (ev.detail && ev.detail.requestConfig &&
			ev.detail.requestConfig.headers[unasked]) {
			return;
		}
		const notice = failedNotice();
		if (notice && !offlineShown()) {
			notice.show();
		}
	});
}
document.addEventListener("htmx:beforeRequest", () => {
	const notice = failedNotice();
	if (notice) {
		notice.close();
	}
});

// The button now shown takes the focus, so a keyboard stays on the control (ADR 50).
document.addEventListener("click", ev => {
	const button = ev.target.closest(".scheme-toggle button");
	if (!button) {
		return;
	}
	const next = button.dataset.next;
	applyScheme(next);
	if (next === "system") {
		sessionStorage.removeItem("scheme");
	} else {
		sessionStorage.setItem("scheme", next);
	}
	if (document.activeElement === button) {
		button.parentElement.querySelector(`[data-scheme="${next}"]`).focus();
	}
});

// The rail stays; only its mark moves, matched by path, account and view.
const railPlace = href => {
	const u = new URL(href, location.href);
	return u.pathname + "|" + (u.searchParams.get("account") || "") + "|" + (u.searchParams.get("view") || "");
};
// A swap that keeps the place, as a page or a sort does, keeps the mark the server drew.
let placeBefore = "";
document.addEventListener("htmx:beforeRequest", () => {
	placeBefore = railPlace(location.href);
});
document.addEventListener("htmx:afterSwap", ev => {
	if (!ev.target || ev.target.id !== "main") {
		return;
	}
	// On a phone the list is under the drawer; a rail link's tap is for the page.
	const from = ev.detail && ev.detail.requestConfig && ev.detail.requestConfig.elt;
	const drawer = document.getElementById("sidebar");
	if (drawer && from && from.matches && from.matches("aside a")) {
		drawer.checked = false;
	}
	const here = railPlace(location.href);
	if (here === placeBefore) {
		return;
	}
	for (const marked of document.querySelectorAll("aside li.active, aside summary.active")) {
		marked.classList.remove("active");
	}
	// An account's name and its inbox share an address; the inbox takes the mark.
	const rows = [...document.querySelectorAll("aside a[href]")]
		.filter(link => railPlace(link.href) === here)
		.map(link => link.closest("summary, li"))
		.filter(row => row);
	const row = rows.find(row => !row.classList.contains("aside-account")) || rows[0];
	if (row) {
		row.classList.add("active");
	}
});

// Back shows the page gone back to at once, from this document's memory, and
// the server's answer for it follows (ADR 13). Left to htmx, the page being
// left stays on screen under the address gone back to until the server answers.
// As many pages as htmx keeps by default.
const backPages = 10;
const kept = new Map();
const pathOf = path => {
	const u = new URL(path, location.href);
	return u.pathname + u.search;
};
let shown = pathOf(location.href);
let restoring = null;
document.addEventListener("htmx:pushedIntoHistory", ev => {
	shown = pathOf(ev.detail.path);
});
document.addEventListener("htmx:replacedInHistory", ev => {
	shown = pathOf(ev.detail.path);
});
// The page as the server sent it: the fit's moves undone, and nothing of a request or a notice still running.
document.addEventListener("htmx:beforeHistorySave", ev => {
	const wraps = [...document.querySelectorAll(".actions-wrap")].filter(wrap => fits.has(wrap));
	for (const wrap of wraps) {
		fits.get(wrap).unfit();
	}
	const page = ev.detail.historyElt.cloneNode(true);
	for (const wrap of wraps) {
		fits.get(wrap).fit();
	}
	for (const el of page.querySelectorAll(".progress, .notices > div.notice")) {
		el.remove();
	}
	for (const el of page.querySelectorAll(".notices > dialog[open]")) {
		el.removeAttribute("open");
	}
	for (const el of page.querySelectorAll("[aria-busy]")) {
		el.removeAttribute("aria-busy");
	}
	for (const el of page.querySelectorAll("[data-busy]")) {
		el.removeAttribute("data-busy");
		el.disabled = false;
	}
	kept.delete(shown);
	kept.set(shown, { html: page.innerHTML, title: document.title, scroll: window.scrollY });
	if (kept.size > backPages) {
		kept.delete(kept.keys().next().value);
	}
});
document.addEventListener("htmx:historyCacheMiss", ev => {
	if (restoring) {
		restoring.abort();
		restoring = null;
	}
	shown = pathOf(ev.detail.path);
	const page = kept.get(shown);
	if (!page) {
		return;
	}
	restoring = ev.detail.xhr;
	htmx.swap(ev.detail.historyElt, page.html, { swapStyle: "innerHTML", swapDelay: 0, settleDelay: 0 },
		{ contextElement: ev.detail.historyElt, title: page.title });
	window.scrollTo(0, page.scroll);
});
// Once the reader acts on the kept page, the server's late answer would undo it or take them back.
for (const event of ["htmx:beforeHistoryUpdate", "pointerdown", "keydown"]) {
	document.addEventListener(event, () => {
		if (restoring) {
			restoring.abort();
			restoring = null;
		}
	});
}
for (const event of ["htmx:historyRestore", "htmx:historyCacheMissLoadError"]) {
	document.addEventListener(event, () => {
		restoring = null;
	});
}

// Mail arriving renews the rail; what the page asks for by itself says so, or it would hold the lock off for ever.
const rail = document.querySelector("aside");
if (rail && window.EventSource) {
	let due = null;
	const live = new EventSource(document.body.dataset.events);

	const listing = () => document.querySelector("main.message-list [data-live-folder]");

	// A merged list holds every account's folder of that name.
	const shows = (here, account, folder) =>
		here.dataset.liveFolder === folder &&
		(here.dataset.liveMerged === "1" || here.dataset.liveAccount === "" || here.dataset.liveAccount === account);

	// The rows under the reader's hands are theirs; otherwise the bar offers the renewal.
	const undisturbed = here =>
		window.scrollY === 0 &&
		// The row checkboxes belong to the form by name, not by nesting.
		!document.querySelector("input[type=checkbox][form=messages-form]:checked") &&
		!document.querySelector("[popover]:popover-open") &&
		!new URL(location.href).searchParams.get("page") &&
		// The search field sits in the toolbar, beside the rows.
		!here.closest("main").querySelector("input:focus, textarea:focus");

	const renew = (here, headers) => {
		if (!window.htmx) {
			return;
		}
		notices.live.hide();
		htmx.ajax("GET", location.href, { source: here, target: "#main", swap: "innerHTML", headers });
	};

	const offer = here => {
		const bar = notices.live.show(rail.dataset.liveArrived || "");
		bar.querySelector("progress").hidden = true;
		const button = document.createElement("button");
		button.className = "button-link notice-action";
		button.textContent = rail.dataset.liveShow || "";
		button.addEventListener("click", () => renew(listing() || here));
		bar.append(button);
	};

	// A lock taken in another tab.
	live.addEventListener("locked", () => location.reload());
	// A stream the server closed for good; the lock's script knows how to ask.
	live.addEventListener("error", () => {
		if (live.readyState === EventSource.CLOSED) {
			document.dispatchEvent(new Event("alborz:streamclosed"));
		}
	});

	// Back in mail, the page landed on was fetched after the mail came.
	let away = false;
	document.addEventListener("htmx:afterSwap", () => {
		if (!document.querySelector("aside[data-mail-sidebar]")) {
			away = true;
			return;
		}
		if (away) {
			notices.live.hide();
		}
		away = false;
	});

	live.addEventListener("mailbox", ev => {
		const [account, ...rest] = String(ev.data || "").split(" ");
		const folder = rest.join(" ");
		// Mail arrives in bursts; the counts are fetched once per burst.
		clearTimeout(due);
		due = setTimeout(() => {
			const here = document.querySelector("aside[data-mail-sidebar]");
			if (here && window.htmx) {
				const url = new URL(here.dataset.mailSidebar, location.origin);
				url.search = location.search;
				url.searchParams.set("path", location.pathname);
				htmx.ajax("GET", url.href,
					{ source: here, target: here, select: "aside", swap: "outerHTML", headers: { [unasked]: "1" } });
			}
			const list = listing();
			if (!list || !shows(list, account, folder)) {
				return;
			}
			if (undisturbed(list)) {
				renew(list, { [unasked]: "1" });
			} else {
				offer(list);
			}
		}, 2000);
	});
}

// A swap brings a new notice, hidden as the server wrote it, so the state is applied again.
if ("serviceWorker" in navigator) {
	let offline = false;
	let slow = false;
	const showOffline = () => {
		const notice = document.getElementById("offline-notice");
		if (notice) {
			notice.querySelector(".notice-text").textContent = slow ? notice.dataset.slow : notice.dataset.offline;
			offline ? notice.show() : notice.close();
		}
		const failed = failedNotice();
		if (offline && failed) {
			failed.close();
		}
	};
	navigator.serviceWorker.addEventListener("message", ev => {
		if (ev.data && "alborzOffline" in ev.data) {
			offline = ev.data.alborzOffline;
			slow = ev.data.alborzSlow;
			showOffline();
		}
	});
	document.addEventListener("htmx:load", showOffline);
	navigator.serviceWorker.ready.then(registration => {
		if (registration.active) {
			registration.active.postMessage("alborz-offline?");
		}
	});
}

// The installed app's badge follows the rail's count; a page without the mail rail leaves it as it was.
if (navigator.setAppBadge) {
	const badge = () => {
		const rail = document.querySelector("aside[data-unread]");
		if (rail) {
			const unread = Number(rail.dataset.unread);
			(unread ? navigator.setAppBadge(unread) : navigator.clearAppBadge()).catch(() => {});
		}
	};
	document.addEventListener("htmx:load", badge);
	badge();
}

if ("serviceWorker" in navigator) {
	window.addEventListener("load", () => {
		// Revalidate the worker's script on every registration, so a new shell arrives on the next visit.
		navigator.serviceWorker.register(document.body.dataset.worker, { updateViaCache: "none" }).catch(() => {});
	});
}

// A form is sent once. Its buttons dim a tick later, after the clicked one is read into the form data.
const submitButtons = form => {
	const inside = form.querySelectorAll('button:not([type="button"]), input[type="submit"]');
	if (!form.id) {
		return inside;
	}
	return [...inside, ...document.querySelectorAll(`button[form="${form.id}"]`)];
};
const setBusy = form => {
	form.dataset.busy = "1";
	form.setAttribute("aria-busy", "true");
	setTimeout(() => {
		for (const button of submitButtons(form)) {
			if (!button.disabled) {
				button.disabled = true;
				button.dataset.busy = "1";
			}
		}
	}, 0);
};
const clearBusy = form => {
	delete form.dataset.busy;
	form.removeAttribute("aria-busy");
	for (const button of submitButtons(form)) {
		if (button.dataset.busy) {
			button.disabled = false;
			delete button.dataset.busy;
		}
	}
};
document.addEventListener("submit", ev => {
	const form = ev.target;
	// A boosted form never leaves the page; htmx's events handle it.
	if (ev.defaultPrevented) {
		return;
	}
	// A download never leaves the page, so it would never end the busy state.
	if ("download" in form.dataset || (ev.submitter && "download" in ev.submitter.dataset)) {
		return;
	}
	if (form.dataset.busy) {
		ev.preventDefault();
		return;
	}
	setBusy(form);
});
document.addEventListener("htmx:beforeRequest", ev => {
	const form = ev.detail.elt;
	if (form.tagName === "FORM") {
		setBusy(form);
	}
});
document.addEventListener("htmx:afterRequest", ev => {
	const form = ev.detail.elt;
	if (form.tagName === "FORM") {
		clearBusy(form);
	}
});
window.addEventListener("pageshow", ev => {
	if (!ev.persisted) {
		return;
	}
	for (const form of document.querySelectorAll("form[data-busy]")) {
		delete form.dataset.busy;
		form.removeAttribute("aria-busy");
	}
	for (const button of document.querySelectorAll("[data-busy]")) {
		button.disabled = false;
		delete button.dataset.busy;
	}
});

// Nothing appears for a quick answer: a flash on every click is noise.
const progressDelay = 150;
let progressTimer = null;
let inFlight = 0;

// A boosted swap replaces the body and the bar with it.
const progressBar = () => {
	let bar = document.querySelector(".progress");
	if (!bar) {
		bar = document.createElement("div");
		bar.className = "progress";
		bar.hidden = true;
		document.body.appendChild(bar);
	}
	return bar;
};

const showProgress = () => {
	const progress = progressBar();
	progress.hidden = false;
	// A bar that reached the end on its own would promise an arrival nobody has.
	requestAnimationFrame(() => progress.classList.add("progress-running"));
};

const endProgress = () => {
	clearTimeout(progressTimer);
	progressTimer = null;
	const progress = progressBar();
	progress.classList.remove("progress-running");
	progress.classList.add("progress-done");
	setTimeout(() => {
		progress.hidden = true;
		progress.classList.remove("progress-done");
	}, 200);
};

// What the page asks for by itself is not a request the reader waits on.
const askedByPage = ev => Boolean(ev.detail && ev.detail.requestConfig && ev.detail.requestConfig.headers[unasked]);

document.addEventListener("htmx:beforeRequest", ev => {
	if (askedByPage(ev)) {
		return;
	}
	inFlight++;
	const target = ev.detail && ev.detail.target;
	if (target && target.setAttribute) {
		target.setAttribute("aria-busy", "true");
	}
	if (progressTimer === null && progressBar().hidden) {
		progressTimer = setTimeout(showProgress, progressDelay);
	}
});

for (const event of ["htmx:afterRequest", "htmx:sendError", "htmx:timeout", "htmx:responseError"]) {
	document.addEventListener(event, ev => {
		if (askedByPage(ev)) {
			return;
		}
		inFlight = Math.max(0, inFlight - 1);
		const target = ev.detail && ev.detail.target;
		if (target && target.removeAttribute) {
			target.removeAttribute("aria-busy");
		}
		if (inFlight === 0) {
			endProgress();
		}
	});
}

// The page leaves with the lock, so what is on screen does not outstay it.
const lock = document.querySelector('meta[name="alborz-lock"]');
const lockAfter = Number((lock || {}).content);
if (lockAfter > 0 && location.pathname !== lock.dataset.unlock) {
	// Writing a long message asks the server nothing, so it is told the reader is here.
	const tellEvery = 60;
	let timer, told = Date.now();
	const leave = () => location.assign(lock.dataset.unlock + "?next=" + encodeURIComponent(location.pathname + location.search));
	// The lock may have been taken in another tab; fetch follows its redirect silently.
	const tell = () => fetch(lock.dataset.alive, {method: "POST"}).then(answer => {
		if (answer.redirected && new URL(answer.url).pathname === lock.dataset.unlock) {
			leave();
		}
	}).catch(() => {});
	const arm = () => {
		if (Date.now() - told > tellEvery * 1000) {
			told = Date.now();
			tell();
		}
		clearTimeout(timer);
		timer = setTimeout(leave, lockAfter * 1000);
	};
	document.addEventListener("alborz:streamclosed", tell);
	for (const event of ["pointerdown", "keydown"]) {
		document.addEventListener(event, arm, {passive: true});
	}
	document.addEventListener("htmx:afterRequest", ev => {
		if (!ev.detail.requestConfig.headers[unasked]) {
			arm();
		}
	});
	arm();
	// A page from the history cache never asked the server whether it locked since.
	window.addEventListener("pageshow", event => {
		if (event.persisted) {
			location.reload();
		}
	});
}

// One colours menu serves every row's star; the chevron that opens it names the row.
let starForm = null;
document.addEventListener("click", ev => {
	const chevron = ev.target.closest("[data-row-colors]");
	if (chevron) {
		starForm = chevron.closest("form");
		return;
	}
	const pick = ev.target.closest("#row-colors [data-color]");
	if (!pick || !starForm) {
		return;
	}
	const color = document.createElement("input");
	color.type = "hidden";
	color.name = "color";
	color.value = pick.value;
	starForm.append(color);
	document.getElementById("row-colors").hidePopover();
	starForm.requestSubmit();
});

// Picking the given colour again clears it, as pressing a lit star does.
document.addEventListener("beforetoggle", ev => {
	const menu = ev.target;
	if (!menu.classList || !menu.classList.contains("flag-colors")) {
		return;
	}
	const owner = menu.id === "row-colors" ? starForm : menu.closest(".flag-split");
	const split = owner && (owner.matches(".flag-split") ? owner : owner.querySelector(".flag-split"));
	if (menu.id === "row-colors" && starForm) {
		starForm.classList.toggle("star-open", ev.newState === "open");
	}
	if (ev.newState !== "open" || !split) {
		return;
	}
	const current = split.dataset.current;
	for (const button of menu.querySelectorAll("[data-color]")) {
		const given = button.dataset.color !== "" && button.dataset.color === current;
		button.value = given ? "" : button.dataset.color;
		button.toggleAttribute("aria-current", given);
		button.classList.toggle("is-current", given);
	}
}, true);

// A bar per source of news: mail arriving must not take an upload's progress.
// As long as a done notice from the server stays (ADR 41).
const noticeLinger = 6000;
const noticeBar = () => {
	let bar = null;
	let leaving = 0;
	const place = () => {
		const layer = document.querySelector(".notices");
		if (layer) {
			layer.prepend(bar);
		}
	};
	return {
		show(text, kind) {
			clearTimeout(leaving);
			if (!bar) {
				bar = document.createElement("div");
				bar.innerHTML = "<span class=\"notice-text\"></span><progress max=\"100\"></progress>";
				place();
			}
			bar.className = "notice notice-" + (kind || "");
			bar.setAttribute("role", kind === "failed" ? "alert" : "status");
			bar.querySelector(".notice-text").textContent = text;
			for (const old of bar.querySelectorAll("button")) {
				old.remove();
			}
			return bar;
		},
		hide() {
			clearTimeout(leaving);
			if (bar) {
				bar.remove();
				bar = null;
			}
		},
		linger() {
			clearTimeout(leaving);
			leaving = setTimeout(() => this.hide(), noticeLinger);
		},
		// A swap takes the bar with it, though what it reports is still going on.
		reattach() {
			if (bar && !bar.isConnected) {
				place();
			}
		},
	};
};
const notices = { live: noticeBar(), upload: noticeBar() };

(() => {
	const rail = () => document.querySelector("aside");
	// A view's or a merged role's row cannot say where a file should land.
	const folderLink = node => node && node.closest && node.closest("aside a[data-folder]");
	const mailArea = node => node && node.closest && node.closest("aside[data-mail-sidebar], main.message-list");
	// The folder is named inside the list, by the part every swap renews.
	const dropMbox = list => list.querySelector("[data-drop-mbox]");
	const listArea = node => {
		const list = node && node.closest && node.closest("main.message-list");
		return list && dropMbox(list) ? list : null;
	};
	const target = node => folderLink(node) || listArea(node);
	const folderURL = el => el.dataset.folder || dropMbox(el).dataset.dropMbox;
	const importURL = el => el.dataset.import || dropMbox(el).dataset.dropImport;
	// Refusing the drop for the page would refuse it for a file field too.
	const ownField = node => node && node.closest && node.closest('input[type="file"]');
	const carriesFiles = ev => ev.dataTransfer && [...ev.dataTransfer.types].includes("Files") && !ownField(ev.target);
	const say = key => (rail() && rail().dataset[key]) || "";
	const status = (text, kind) => notices.upload.show(text, kind).querySelector("progress");
	const done = () => notices.upload.hide();

	document.addEventListener("htmx:afterSwap", () => {
		notices.upload.reattach();
		notices.live.reattach();
	});

	// Otherwise the browser opens a dropped file itself, leaving the page.
	window.addEventListener("dragenter", ev => {
		if (carriesFiles(ev)) {
			ev.preventDefault();
		}
	});

	document.addEventListener("dragover", ev => {
		if (!carriesFiles(ev)) {
			return;
		}
		ev.preventDefault();
		const into = target(ev.target);
		if (into) {
			into.classList.add("drop-into");
		}
	});

	document.addEventListener("dragleave", ev => {
		const into = target(ev.target);
		// Crossing from one row to the next leaves a child, not the area.
		if (into && !into.contains(ev.relatedTarget)) {
			into.classList.remove("drop-into");
		}
	});

	document.addEventListener("drop", ev => {
		if (!carriesFiles(ev)) {
			return;
		}
		ev.preventDefault();
		const into = target(ev.target);
		if (into) {
			into.classList.remove("drop-into");
		}
		// A dropped directory carries no bytes; Apple Mail exports a mailbox as a .mbox folder.
		const entries = [...ev.dataTransfer.items]
			.map(item => item.webkitGetAsEntry && item.webkitGetAsEntry())
			.filter(Boolean);
		const dropped = [...ev.dataTransfer.files];
		const waiting = entries.length ? collect(entries) : Promise.resolve(dropped);
		if (into) {
			send(new URL(folderURL(into), location.origin), new URL(importURL(into), location.origin), waiting);
			return;
		}
		// A merged list names the folder, not the account; the rail knows which accounts hold it.
		const accounts = holders();
		if (accounts.length === 1) {
			send(new URL(accounts[0].href, location.origin), new URL(accounts[0].into, location.origin), waiting);
			return;
		}
		if (accounts.length > 1) {
			ask(accounts, waiting);
			return;
		}
		if (mailArea(ev.target)) {
			status(say("importingWhere"), "failed").hidden = true;
			notices.upload.linger();
		}
	});

	const holders = () => [...document.querySelectorAll("aside a[data-folder]")]
		.map(row => ({href: row.dataset.folder, into: row.dataset.import, label: row.textContent.trim(), account: new URL(row.dataset.folder, location.origin).searchParams.get("account")}))
		.filter(row => row.account && new URL(row.href, location.origin).pathname === location.pathname);

	const ask = (accounts, waiting) => {
		const here = document.querySelector("[data-live-folder]").dataset.liveFolder;
		const bar = notices.upload.show(say("importingWhich").replace("%s", here));
		bar.querySelector("progress").hidden = true;
		for (const account of accounts) {
			const button = document.createElement("button");
			button.className = "button-link notice-action";
			button.textContent = account.account;
			button.addEventListener("click", () => {
				// The files go once, to one account.
				for (const other of bar.querySelectorAll("button")) {
					other.disabled = true;
				}
				send(new URL(account.href, location.origin), new URL(account.into, location.origin), waiting);
			});
			bar.append(button);
		}
	};

	// An Apple .mbox folder also holds Info.plist, a table of contents and .emlx copies.
	const mailLike = file => /^mbox$|\.(mbox|eml)$/i.test(file.name);

	const collect = async entries => {
		const files = [];
		const pending = [...entries];
		while (pending.length) {
			const entry = pending.shift();
			if (entry.isFile) {
				files.push(await new Promise((ok, no) => entry.file(ok, no)));
				continue;
			}
			const reader = entry.createReader();
			for (;;) {
				const batch = await new Promise((ok, no) => reader.readEntries(ok, no));
				if (!batch.length) {
					break;
				}
				pending.push(...batch);
			}
		}
		return files;
	};

	const post = (into, batch, before, total) => new Promise(resolve => {
		const body = new FormData();
		for (const file of batch) {
			body.append("file", file);
		}
		const naming = batch.length === 1
			? say("importing").replace("%s", batch[0].name)
			: say("importingMany").replace("%s", new Intl.NumberFormat(document.documentElement.lang || "en").format(batch.length));
		const progress = status(naming);
		const request = new XMLHttpRequest();
		request.open("POST", into.pathname + into.search);
		request.upload.addEventListener("progress", e => {
			if (e.lengthComputable) {
				progress.value = Math.round(((before + (e.loaded / e.total) * batch.length) / total) * 100);
			}
		});
		// Uploaded, not yet answered: how long appending takes is the server's to know.
		request.upload.addEventListener("load", () => {
			progress.removeAttribute("value");
			status(say("importingAdding"));
		});
		request.addEventListener("loadend", () => resolve(request.status));
		request.send(body);
	});

	const send = async (href, into, waiting) => {
		const found = await waiting;
		const files = found.filter(mailLike);
		if (!files.length) {
			if (found.length) {
				status(say("importingNone"), "failed").hidden = true;
				notices.upload.linger();
			}
			return;
		}
		// A reload would take the upload down midway.
		const hold = ev => ev.preventDefault();
		window.addEventListener("beforeunload", hold);
		// A proxy in front may answer 413 without naming its limit, so a refused batch is halved.
		const pending = [files];
		let landed = 0;
		let refused = 0;
		let failure = 0;
		while (pending.length) {
			const batch = pending.shift();
			const code = await post(into, batch, landed + refused, files.length);
			if (code >= 200 && code <= 299) {
				landed += batch.length;
				continue;
			}
			if (code === 413 && batch.length > 1) {
				const half = Math.ceil(batch.length / 2);
				pending.unshift(batch.slice(0, half), batch.slice(half));
				continue;
			}
			refused += batch.length;
			failure = code;
		}
		window.removeEventListener("beforeunload", hold);
		if (refused) {
			status(say("importingFailed").replace("%s", failure || "-"), "failed").hidden = true;
			return;
		}
		done();
		// Reloading the page they were on would show nothing new.
		location.assign(href.href);
	};
})();

// A status bar the page does not draw, as on a phone, asks for nothing.
document.addEventListener("htmx:confirm", ev => {
	const segment = ev.target.closest(".status-segment");
	if (segment && !segment.checkVisibility()) {
		ev.preventDefault();
	}
});

// The header's search, as the reader types. Without it the dialog's form is a plain GET of the search page.
(() => {
	// Long enough to wait out a word being typed; short enough to read as instant.
	const TYPED_MS = 250;
	// A section that asks a server is asked once the reader stops, or at Enter: typing asks nothing of it.
	const PAUSED_MS = 1000;

	let typed = 0;
	let paused = 0;
	// The query asked, and the controller that withdraws its requests when the next one starts.
	let run = null;

	const parts = () => {
		const dialog = document.getElementById("search-dialog");
		if (!dialog) {
			return null;
		}
		return {
			dialog,
			form: dialog.querySelector("form"),
			input: dialog.querySelector('input[name="query"]'),
			found: dialog.querySelector(".search-dialog-found"),
		};
	};

	const blocks = found => [...found.querySelectorAll("section[data-section]")];

	// Only the dialog asks for a fragment, and names itself as htmx would.
	const ask = (p, block, extra) => {
		const params = new URLSearchParams(new FormData(p.form));
		params.set("section", block.dataset.section);
		for (const [k, v] of Object.entries(extra)) {
			params.set(k, v);
		}
		// A section asked twice for one query shows the last answer asked for, whichever arrives last.
		const current = run;
		const token = {};
		current.latest[block.dataset.section] = token;
		block.setAttribute("aria-busy", "true");
		fetch(p.form.action + "?" + params, {
			signal: current.ctrl.signal,
			headers: { "HX-Request": "true", "HX-Target": p.dialog.id },
		})
			.then(r => r.ok ? r.text() : Promise.reject(new Error(r.status)))
			.then(html => {
				const t = document.createElement("template");
				t.innerHTML = html;
				const answer = t.content.querySelector("section[data-section]");
				const now = p.found.querySelector(`section[data-section="${CSS.escape(block.dataset.section)}"]`);
				if (answer && now && current === run && current.latest[block.dataset.section] === token) {
					now.replaceWith(answer);
					if (window.htmx) {
						htmx.process(answer);
					}
				}
			})
			.catch(err => {
				if (err.name === "AbortError" || current !== run || current.latest[block.dataset.section] !== token) {
					return;
				}
				const now = p.found.querySelector(`section[data-section="${CSS.escape(block.dataset.section)}"]`);
				if (now) {
					const note = document.createElement("p");
					note.className = "hint list-option";
					note.textContent = p.found.dataset.failed;
					now.replaceChildren(now.querySelector(".search-kind"), note);
					now.removeAttribute("aria-busy");
				}
			});
	};

	const start = p => {
		const query = p.input.value.trim();
		if (run && run.query === query) {
			return;
		}
		if (run) {
			run.ctrl.abort();
		}
		run = { query, ctrl: new AbortController(), latest: {} };
		p.found.hidden = query === "";
		if (query === "") {
			return;
		}
		for (const block of blocks(p.found)) {
			ask(p, block, "remote" in block.dataset ? { held: "1" } : {});
		}
	};

	const full = p => {
		clearTimeout(paused);
		if (!run || run.query === "") {
			return;
		}
		for (const block of blocks(p.found)) {
			if ("remote" in block.dataset) {
				ask(p, block, {});
			}
		}
	};

	document.addEventListener("input", ev => {
		const p = parts();
		if (!p || ev.target !== p.input) {
			return;
		}
		clearTimeout(typed);
		clearTimeout(paused);
		typed = setTimeout(() => start(p), TYPED_MS);
		paused = setTimeout(() => { start(p); full(p); }, PAUSED_MS);
	});

	// Captured, so the form is not marked as sent: it never leaves the page.
	document.addEventListener("submit", ev => {
		const p = parts();
		if (!p || ev.target !== p.form) {
			return;
		}
		ev.preventDefault();
		clearTimeout(typed);
		start(p);
		full(p);
	}, true);

	// The field is where the dialog opens, with what was typed last ready to be replaced.
	document.addEventListener("command", ev => {
		const p = parts();
		if (p && ev.target === p.dialog) {
			requestAnimationFrame(() => p.input.select());
		}
	}, true);

	// Arrows walk the field and each result's link; Enter opens the link, as it would anywhere.
	document.addEventListener("keydown", ev => {
		const p = parts();
		if (!p || !p.dialog.open || (ev.key !== "ArrowDown" && ev.key !== "ArrowUp") || ev.isComposing) {
			return;
		}
		const stops = [p.input, ...p.found.querySelectorAll(".list-primary a, .list-option a")];
		const at = stops.indexOf(document.activeElement);
		if (at < 0) {
			return;
		}
		ev.preventDefault();
		const to = Math.min(stops.length - 1, Math.max(0, at + (ev.key === "ArrowDown" ? 1 : -1)));
		stops[to].focus();
		stops[to].scrollIntoView({ block: "nearest" });
	});
})();

// @license-end
