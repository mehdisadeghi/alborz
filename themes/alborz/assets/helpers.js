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

const check_all = document.getElementById("action-checkbox-all");
if (check_all) {
	check_all.style.display = "inherit";
	check_all.addEventListener("click", ev => {
		const inputs = document.querySelectorAll(".message-list-checkbox input");
		for (let i = 0; i < inputs.length; i++) {
			inputs[i].checked = ev.target.checked;
		}
	});
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
