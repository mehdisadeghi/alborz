// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

{
	const bound = new WeakSet();
	const enhanceEventForm = () => {
		const toggle = document.getElementById("allday");
		if (!toggle) return;
		const update = () => {
			for (const name of ["start", "end", "start_date", "end_date"]) {
				const disabled = name.endsWith("_date") !== toggle.checked;
				for (const key of [name, `${name}-calendar`]) {
					toggle.form.elements.namedItem(key).disabled = disabled;
				}
			}
		};
		if (!bound.has(toggle)) {
			bound.add(toggle);
			toggle.addEventListener("change", update);
		}
		// Refresh even a bound form: an enhancement may have replaced a control.
		update();
	};
	enhanceEventForm();
	document.addEventListener("htmx:load", enhanceEventForm);
}

// @license-end
