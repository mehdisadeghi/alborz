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
	const update = () => {
		const any = Array.prototype.some.call(boxes, box => box.checked);
		for (const control of controls) {
			control.disabled = !any;
		}
	};
	for (const box of boxes) {
		box.addEventListener("change", update);
	}
	if (check_all) {
		// Shown either way: a list with nothing in it would otherwise
		// drop the cell and slide the whole toolbar sideways.
		// Drop the inline rule the markup carries rather than writing
		// another one over it: display:inherit takes whatever the cell
		// happens to compute to, which is not the same box in every
		// engine, and left the control invisible in Firefox.
		check_all.style.removeProperty("display");
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

// @license-end
