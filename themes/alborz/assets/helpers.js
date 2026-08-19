// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// Set timezone cookie for server-side date formatting
try {
	const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
	if (tz && document.cookie.indexOf("timezone=") === -1) {
		document.cookie = "timezone=" + tz + ";path=/;max-age=31536000;SameSite=Lax";
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

// Bulk actions operate on the checked rows: the select-all box appears,
// and the action buttons are disabled while nothing is selected.
const check_all = document.getElementById("action-checkbox-all");
for (const formId of ["messages-form", "address-book-form"]) {
	const boxes = document.querySelectorAll(`input[type="checkbox"][form="${formId}"]`);
	if (boxes.length === 0) {
		continue;
	}
	const buttons = document.querySelectorAll(`button[form="${formId}"]`);
	const update = () => {
		const any = Array.prototype.some.call(boxes, box => box.checked);
		for (const button of buttons) {
			button.disabled = !any;
		}
	};
	for (const box of boxes) {
		box.addEventListener("change", update);
	}
	if (check_all) {
		check_all.style.display = "inherit";
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
