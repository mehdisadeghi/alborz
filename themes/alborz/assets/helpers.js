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

// An empty timezone field shows the browser's own zone as its placeholder.
// This lives here because the page CSP has no unsafe-inline for scripts.
const tz_input = document.getElementById("timezone");
if (tz_input && !tz_input.value) {
	try {
		tz_input.placeholder = Intl.DateTimeFormat().resolvedOptions().timeZone;
	} catch (e) {}
}

// Bulk actions operate on the checked rows: the select-all box appears
// where there are rows, and every control bound to the bulk form is
// disabled while nothing is selected - the move destination as much as
// the buttons, and an empty list as much as an unselected one.
const check_all = document.getElementById("action-checkbox-all");
for (const formId of ["messages-form", "address-book-form"]) {
	const controls = document.querySelectorAll(
		`button[form="${formId}"], select[form="${formId}"]`,
	);
	if (controls.length === 0) {
		continue;
	}
	const boxes = document.querySelectorAll(`input[type="checkbox"][form="${formId}"]`);
	// A menu is not a control the browser can disable: mark it, let CSS
	// dim it and refuse the pointer, and shut it if it was open when the
	// last row was unchecked.
	const menus = document.querySelectorAll(`details[data-gated="${formId}"]`);
	const update = () => {
		const any = Array.prototype.some.call(boxes, box => box.checked);
		for (const control of controls) {
			control.disabled = !any;
		}
		for (const menu of menus) {
			menu.classList.toggle("is-disabled", !any);
			// The mark goes on the summary as well as the menu: the
			// hover rules are guarded against a control's own off
			// state, and CSS cannot ask about an ancestor's.
			const summary = menu.querySelector("summary");
			if (summary) {
				summary.classList.toggle("is-disabled", !any);
				summary.setAttribute("aria-disabled", String(!any));
			}
			if (!any) {
				menu.open = false;
			}
		}
	};
	for (const box of boxes) {
		box.addEventListener("change", update);
	}
	if (check_all) {
		// The markup ships it disabled and says it needs a script; this
		// is the script. From here it is disabled only for the reason
		// every other bulk control is - an empty list.
		check_all.title = check_all.dataset.title || "";
		check_all.disabled = boxes.length === 0;
		check_all.addEventListener("click", ev => {
			for (const box of boxes) {
				box.checked = ev.target.checked;
			}
			update();
		});
	}
	update();
}

// Escape leaves the search: it clears the term and drops focus, which
// also collapses the overlay the small-screen magnifier opens.
for (const search of document.querySelectorAll(".actions-search input")) {
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
		form.submit();
	});
	for (const child of children) {
		child.addEventListener("change", sync);
	}
	sync();
}

const submit_on_change = document.querySelectorAll("[data-submit-on-change]");
for (let i = 0; i < submit_on_change.length; i++) {
	submit_on_change[i].addEventListener("change", ev => {
		ev.currentTarget.form.submit();
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
if (caret) {
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

// @license-end
