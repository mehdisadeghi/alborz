// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// The zone the server formats dates in. This is the one cookie the page
// writes rather than the server, so it cannot be HttpOnly; it carries the
// same name, path, policy and lifetime as the rest either way.
try {
	const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
	if (tz && document.cookie.indexOf("alborz_tz=") === -1) {
		const secure = location.protocol === "https:" ? ";Secure" : "";
		document.cookie =
			"alborz_tz=" + tz + ";path=/;max-age=31536000;SameSite=Strict" + secure;
	}
} catch (e) {}


// The page is enhanced again whenever htmx puts new markup in place, so
// everything below runs more than once against a document that is
// partly new. A listener added twice acts twice, so each is claimed for
// its element by name; the element is forgotten with the DOM node.
// Whether a finger is doing the pointing, which decides what may take
// focus: on a touch screen a focused field is a keyboard over half the
// page and, where the field has a suggestion list, the browser's own
// sheet over the rest.
const touch = window.matchMedia && window.matchMedia("(pointer: coarse)").matches;
if (touch) {
	const opening = document.activeElement;
	if (opening && opening.hasAttribute("autofocus")) {
		opening.blur();
	}
}

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

const enhance = () => {
	// An empty timezone field shows the browser's own zone as its placeholder.
	// This lives here because the page CSP has no unsafe-inline for scripts.
	const tz_input = document.getElementById("timezone");
	if (tz_input && !tz_input.value) {
		try {
			tz_input.placeholder = Intl.DateTimeFormat().resolvedOptions().timeZone;
		} catch (e) {}
	}

	// Clearing a notice needs no round trip where a script runs; the
	// markup's link is the fallback for where none does.
	const notice_dismiss = document.querySelector(".notice-dismiss");
	if (notice_dismiss && once(notice_dismiss, "dismiss")) {
		notice_dismiss.addEventListener("click", ev => {
			ev.preventDefault();
			notice_dismiss.closest(".notice").remove();
		});
	}

	// Bulk actions operate on the checked rows: the select-all box appears
	// where there are rows, and every control bound to the bulk form is
	// disabled while nothing is selected - the move destination as much as
	// the buttons, and an empty list as much as an unselected one. A list
	// is any form its rows' checkboxes name, so a new list joins the rule
	// by binding its rows and its actions to one form, and nothing here
	// has to learn its name.
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
		// A menu's button is a real button: disabled with the rest, and its
		// panel shut if it was open when the last row was unchecked.
		const menus = document.querySelectorAll(`[data-gated="${formId}"]`);
		const update = () => {
			const any = Array.prototype.some.call(boxes, box => box.checked);
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
		if (check_all) {
			// The markup ships it disabled and says it needs a script; this
			// is the script. From here it is disabled only for the reason
			// every other bulk control is - an empty list.
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

	// Escape leaves the search: it clears the term and drops focus, which
	// also collapses the overlay the small-screen magnifier opens.
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

	// Creation has no fixed account context: remember one destination per
	// object type in this browser, while the optgroups continue to show the
	// account namespace only once when the menu is opened.
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

	// A pooled DAV account is a source group, not an active-account mode.
	// Its parent checkbox reflects and controls the collection checkboxes in
	// that one account form; the children remain ordinary no-JS controls.
	for (const form of document.querySelectorAll("form[data-source-group]")) {
		if (!once(form, "sourcegroup")) {
			continue;
		}
		const parent = form.querySelector("[data-source-account-toggle]");
		const children = form.querySelectorAll("[data-source-item]");
		if (!parent || children.length === 0) {
			continue;
		}
		const sync = () => {
			const checked = Array.prototype.filter.call(children, child => child.checked).length;
			parent.checked = checked === children.length;
			parent.indeterminate = checked > 0 && checked < children.length;
		};
		parent.addEventListener("change", () => {
			for (const child of children) {
				child.checked = parent.checked;
			}
			// requestSubmit, not submit: the submit event has listeners.
			form.requestSubmit();
		});
		for (const child of children) {
			child.addEventListener("change", sync);
		}
		sync();
	}

	const submit_on_change = document.querySelectorAll("[data-submit-on-change]");
	for (let i = 0; i < submit_on_change.length; i++) {
		if (!once(submit_on_change[i], "submitonchange")) {
			continue;
		}
		submit_on_change[i].addEventListener("change", ev => {
			ev.currentTarget.form.requestSubmit();
		});
		const button = submit_on_change[i].form.querySelector("button");
		if (button) {
			button.style.display = "none";
		}
	}

	// The cursor belongs where the reply is written, which is above the
	// quote or below it. A browser focusing a textarea puts it at one end
	// or the other depending on the browser; this says which end.
	const caret = document.querySelector("textarea[data-caret]");
	if (caret && !touch && once(caret, "caret")) {
		const at = caret.dataset.caret === "start" ? 0 : caret.value.length;
		caret.focus();
		caret.setSelectionRange(at, at);
	}

	// A recipient field holds a list, and a datalist matches the whole
	// value: once "a@b.example, " is typed nothing matches any more, so the
	// completion appears to stop working after the first address. Rewriting
	// each option to "what is already typed" + candidate puts the browser's
	// own matching back on the token being written.
	//
	// Purely additive: without this the first address still completes, and
	// nothing here is the only way to reach an address.
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
					// The label stays the address alone; only the value
					// carries what is already in the field.
					option.label = v;
					return option;
				}));
			});
		}
	}

	// A browser can be told this application opens mail links, and calendar
	// links. It asks only on a user gesture, so each hangs off a button,
	// and the buttons appear only where the browser offers the ability at
	// all.
	const handler_group = document.getElementById("handler-group");
	if (handler_group && navigator.registerProtocolHandler && once(handler_group, "handlers")) {
		handler_group.hidden = false;
		// No API says whether the browser agreed; the most the page can
		// remember is that it asked, so a second visit does not invite a
		// second press as if nothing had happened.
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
				// Refused for want of a secure context, which is worth
				// saying rather than leaving the button looking broken.
				document.getElementById("register-mailto-refused").hidden = false;
			}
		});
	}

	// The rail remembers which account groups the reader left open. Each
	// keyed <details> restores its state on load and saves it on toggle;
	// storage that is unavailable simply means the server default stands.
	// A change made on the rail - a collection ticked, Apply pressed -
	// reloads the page, and on a phone the drawer came back closed with
	// the rail out of sight. A form submitted from the drawer opens it
	// again on the page that follows; a link from the rail does not, since
	// its page is the reason for the tap.
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

	// Each prime nav remembers the place you last were in its section:
	// a folder, a month, a view. Not the account - the account is the
	// scope, the URL carries it (ADR 0001), and a remembered one is
	// wrong twice over: it outlives the account it names, and it
	// overrules the account the reader is looking at right now. So a
	// place is stored stripped of it and rescoped to whatever the
	// current page is scoped to. Only a list page is a place; see
	// nav.html.
	(() => {
		const nav = document.querySelector("header nav[data-here]");
		if (!nav) {
			return;
		}
		const key = section => "nav-place:" + section;
		// An address reads better unescaped, and every link the server
		// writes leaves the at sign alone.
		const scoped = (place, account) => {
			const url = new URL(place, location.origin);
			url.searchParams.delete("account");
			if (account) {
				url.searchParams.set("account", account);
			}
			return url.pathname + url.search.replace(/%40/g, "@");
		};
		const here = nav.dataset.here;
		const account = new URLSearchParams(location.search).get("account");
		if (here && "place" in nav.dataset) {
			try {
				localStorage.setItem(key(here), scoped(location.pathname + location.search, null));
			} catch (e) {}
		}
		for (const a of nav.querySelectorAll("a[data-section]")) {
			if (a.dataset.section === here) {
				continue;
			}
			let place = null;
			try {
				place = localStorage.getItem(key(a.dataset.section));
			} catch (e) {}
			if (place !== null) {
				a.setAttribute("href", scoped(place, account));
			}
		}
	})();

	// The section links are words while the five fit the row and icons
	// once they do not. Measured, not set at a width: the words are longer
	// in German and Persian than in English, and a row that scrolls hides
	// the last section.
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

	// Priority-plus toolbar. Every control sits in the row once; the ones
	// that can give width back carry data-priority, and data-yield says
	// how: "hide" drops it, "word" drops its word and keeps the mark, and
	// by default it moves into the overflow menu. Steps are taken lowest
	// priority first until the row holds everything at natural size, and
	// given back as the row widens. The menu keeps its own items below
	// whatever moved in, in row order. No control is duplicated for a
	// width, and the toggle costs nothing while the menu is empty.
	for (const wrap of document.querySelectorAll(".actions-wrap")) {
		const details = wrap.querySelector("div.overflow:not(.flag-menu)");
		const menu = details && details.querySelector(":scope > .overflow-menu");
		if (!menu) {
			continue;
		}
		const items = Array.prototype.filter.call(wrap.querySelectorAll("[data-priority]"), el => !menu.contains(el))
			.map(el => ({ el, priority: Number(el.dataset.priority), how: el.dataset.yield, parent: el.parentNode, next: el.nextSibling }));
		if (items.length === 0) {
			continue;
		}
		const leaving = items.slice().sort((a, b) => a.priority - b.priority);
		const ownItems = menu.children.length;
		// The title is what flex shrinks, and a shrunk half lets it spill
		// under the other while scrollWidth says nothing. Both halves and the
		// title are held at full size while the row is measured, so what does
		// not fit overflows for real, and the title truncates only once every
		// step has been taken.
		const pinned = wrap.querySelectorAll(":scope > .actions-message, :scope > .actions-end, .actions-message h1");
		const pin = on => { for (const el of pinned) { el.style.flexShrink = on ? "0" : ""; } };
		let fitting = false;
		const fit = () => {
			if (fitting) {
				return;
			}
			fitting = true;
			for (let i = items.length - 1; i >= 0; i--) {
				items[i].el.hidden = false;
				items[i].el.classList.remove("no-word");
				items[i].parent.insertBefore(items[i].el, items[i].next);
			}
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
				if (moved.has(items[i].el)) {
					menu.prepend(items[i].el);
				}
			}
			fitting = false;
		};
		if (once(wrap, "fit")) {
			new ResizeObserver(fit).observe(wrap);
		}
		fit();
	}


	// A browser without anchor positioning would centre every popover in
	// the viewport, which is where the UA sheet puts one. Until Safari and
	// Firefox of last year are gone, the panel is placed under its button
	// by hand when it opens, on the side the menu grows from.
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
// htmx settles new content into the page without a load event of its
// own kind; this is that event.
document.addEventListener("htmx:load", enhance);

// A native suggestion list is the browser's own popup, and on a phone it
// covers the page the moment its field takes focus. Two consequences,
// both handled here rather than by giving up the datalist: a page that
// autofocuses a field should not open a sheet over itself on a touch
// screen, and a popup open when htmx replaces the document's body
// outlives the element it belonged to and hangs there over the next
// page.
document.addEventListener("htmx:beforeSwap", () => {
	const focused = document.activeElement;
	if (focused && focused !== document.body && focused.blur) {
		focused.blur();
	}
});

// htmx throws away anything that is not a 2xx, which is right for a
// server that fell over and wrong for one that answered. A refusal is
// an answer: the login page with its alert, a form with what was typed
// still in it, an edit that met a newer copy. Swap those, and do not
// raise the "no answer" notice for them.
document.addEventListener("htmx:beforeSwap", ev => {
	const status = ev.detail && ev.detail.xhr && ev.detail.xhr.status;
	if (status === 401 || status === 409 || status === 422) {
		ev.detail.shouldSwap = true;
		ev.detail.isError = false;
	}
});

// A request that does not come back must say so: htmx swaps nothing on
// a failure and would otherwise leave a click looking like a click that
// did nothing. The notice is the server's, written into every page and
// hidden; this reveals it, and the next request that does come back
// hides it again. The page itself is left exactly as it was - a POST
// that timed out may still have reached the server, so nothing here
// claims that nothing happened.
// The element is looked up when something happens, not once at load: a
// boosted navigation replaces the body, and a handler holding the first
// page's notice would be pointing at a node no longer in the document -
// which is exactly a failure that shows nothing.
const failedNotice = () => document.getElementById("request-failed");
for (const event of ["htmx:sendError", "htmx:timeout", "htmx:responseError"]) {
	document.addEventListener(event, () => {
		const notice = failedNotice();
		if (notice) {
			notice.hidden = false;
		}
	});
}
document.addEventListener("htmx:beforeRequest", () => {
	const notice = failedNotice();
	if (notice) {
		notice.hidden = true;
	}
});

// The appearance button posts and comes back as itself. What it cannot
// bring with it is the root element's attribute, which is where the
// scheme is actually applied, so that is read off the block the server
// sent rather than worked out here.
document.addEventListener("htmx:afterSwap", ev => {
	const toggle = ev.target.closest ? ev.target.closest(".scheme-toggle") : null;
	if (!toggle) {
		return;
	}
	const scheme = toggle.dataset.scheme;
	if (scheme) {
		document.documentElement.dataset.theme = scheme;
	} else {
		delete document.documentElement.dataset.theme;
	}
});

// A watcher on the server sits in IDLE, so alborz learns that mail
// arrived before anybody asks for a page. What that changes here is the
// rail's count, which is the one place a number for a folder lives; the
// list under the reader's hands is left exactly where it is, and the
// folder they click is already fetched. A reader with no script loses
// nothing they had.
const rail = document.querySelector("aside");
if (rail && window.EventSource) {
	let due = null;
	const live = new EventSource("/events");
	live.addEventListener("mailbox", () => {
		// Mail arrives in bursts; the counts are fetched once for the
		// burst rather than once for each message.
		clearTimeout(due);
		due = setTimeout(() => {
			const here = document.querySelector("aside");
			if (here && window.htmx) {
				htmx.ajax("GET", location.href,
					{ source: here, target: here, select: "aside", swap: "outerHTML" });
			}
		}, 2000);
	});
}

// The worker holds the stylesheet, the scripts and the icons, so the
// application starts without waiting for them, and answers a page that
// cannot be fetched with one that says so. It holds no mail: see
// serviceworker.go.
if ("serviceWorker" in navigator) {
	window.addEventListener("load", () => {
		navigator.serviceWorker.register("/sw.js").catch(() => {});
	});
}

// A form is sent once. Nothing answers a click for the round trip's
// length, so a reader clicks again and the server does the thing
// twice. The form is marked busy on submit and a second submit is
// refused; its buttons dim a tick later, after the browser has read
// the clicked one's name and value into the form data. Coming back
// through the history cache restores the form as it was.
const submitButtons = form => {
	const inside = form.querySelectorAll('button:not([type="button"]), input[type="submit"]');
	if (!form.id) {
		return inside;
	}
	return [...inside, ...document.querySelectorAll(`button[form="${form.id}"]`)];
};
// Both paths end in the same two calls: a form that leaves the page is
// released by the next page, and a boosted one by htmx's answer.
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
	// A boosted form never leaves the page, so htmx's own events say
	// when it is busy and when it is done.
	if (ev.defaultPrevented) {
		return;
	}
	// A download answers without leaving the page, so it never ends the
	// busy state and must not begin one.
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

// The offline page asks again by itself: the browser says when a
// network is back, and a page whose only content is "no connection"
// should not wait to be clicked. The link stays for a browser with no
// script, and stays as the way to try before the network says anything.
const retry = document.getElementById("offline-retry");
if (retry) {
	let asked = false;
	const again = () => {
		if (asked) {
			return;
		}
		asked = true;
		retry.textContent = retry.dataset.retrying || retry.textContent;
		location.replace(retry.href);
	};
	window.addEventListener("online", again);
	// A tab brought back to the front is a reader looking at it again.
	document.addEventListener("visibilitychange", () => {
		if (!document.hidden && navigator.onLine) {
			again();
		}
	});
	// And while it sits there, ask on a slow beat rather than never:
	// a network can come back without the browser saying so.
	setInterval(() => {
		if (navigator.onLine) {
			again();
		}
	}, 15000);
}

// @license-end
