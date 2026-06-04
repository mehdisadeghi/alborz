// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

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
