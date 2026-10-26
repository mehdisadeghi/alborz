// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat
{
const form = document.getElementById("compose-form");
const addresses = [...document.getElementById("emails").options].map(option => option.value);
const lifetime = new AbortController();
const fields = [];

// A quoted display name may contain a comma; only separators outside quotes split addresses.
const tokenAt = (value, caret) => {
	let start = 0, quoted = false, escaped = false;
	for (let i = 0; i < value.length; i++) {
		const char = value[i];
		if (escaped) { escaped = false; continue; }
		if (char === "\\" && quoted) { escaped = true; continue; }
		if (char === '"') quoted = !quoted;
		if (char === "," && !quoted) {
			if (i >= caret) return {start, end: i};
			start = i + 1;
		}
	}
	return {start, end: value.length};
};

for (const input of form.querySelectorAll("[data-recipient]")) {
	const wrap = document.createElement("span");
	wrap.className = "recipient-field";
	input.replaceWith(wrap);
	wrap.append(input);
	const menu = document.createElement("div");
	menu.className = "recipient-suggestions";
	menu.id = input.id + "-suggestions";
	menu.setAttribute("role", "listbox");
	menu.setAttribute("aria-label", input.labels?.[0]?.textContent || input.getAttribute("aria-label"));
	wrap.append(menu);
	input.setAttribute("role", "combobox");
	input.setAttribute("aria-autocomplete", "list");
	input.setAttribute("aria-controls", menu.id);
	let matches = [], active = -1;
	const close = () => {
		menu.hidden = true;
		active = -1;
		input.setAttribute("aria-expanded", "false");
		input.removeAttribute("aria-activedescendant");
	};
	const select = index => {
		active = index;
		for (const [i, row] of [...menu.children].entries()) row.setAttribute("aria-selected", String(i === index));
		input.setAttribute("aria-activedescendant", menu.children[index].id);
		menu.children[index].scrollIntoView({block: "nearest"});
	};
	const choose = index => {
		const token = tokenAt(input.value, input.selectionStart ?? input.value.length);
		const leading = input.value.slice(token.start, token.end).match(/^\s*/)[0];
		const value = leading + matches[index];
		input.setRangeText(value, token.start, token.end, "end");
		close();
		input.dispatchEvent(new Event("input", {bubbles: true}));
		close();
	};
	const show = () => {
		const token = tokenAt(input.value, input.selectionStart ?? input.value.length);
		const query = input.value.slice(token.start, token.end).trim().toLocaleLowerCase();
		close();
		if (!query) return;
		matches = addresses.filter(address => address.toLocaleLowerCase().includes(query) && address.toLocaleLowerCase() !== query).slice(0, 8);
		menu.replaceChildren(...matches.map((address, index) => {
			const row = document.createElement("div");
			row.id = menu.id + "-" + index;
			row.setAttribute("role", "option");
			row.setAttribute("aria-selected", "false");
			row.textContent = address;
			// Keep focus in the combobox; accepting on click still permits scrolling on touch.
			row.addEventListener("pointerdown", event => event.preventDefault());
			row.addEventListener("click", () => choose(index));
			return row;
		}));
		menu.hidden = !matches.length;
		input.setAttribute("aria-expanded", String(matches.length > 0));
	};
	input.addEventListener("input", show);
	input.addEventListener("click", show);
	input.addEventListener("blur", close);
	input.addEventListener("keydown", event => {
		if (event.isComposing) return;
		if (event.key === "Escape") { if (!menu.hidden) event.preventDefault(); close(); return; }
		if (event.key === "ArrowDown" || event.key === "ArrowUp") {
			if (menu.hidden) show();
			if (menu.hidden) return;
			event.preventDefault();
			select((active + (event.key === "ArrowDown" ? 1 : active < 0 ? 0 : -1) + matches.length) % matches.length);
		} else if (event.key === "Enter" && !menu.hidden && active >= 0) {
			event.preventDefault();
			choose(active);
		}
	});
	close();
	fields.push({wrap, close});
}
document.addEventListener("pointerdown", event => {
	for (const field of fields) if (!field.wrap.contains(event.target)) field.close();
}, {signal: lifetime.signal});
document.addEventListener("htmx:beforeSwap", () => fields.forEach(field => field.close()), {signal: lifetime.signal});
window.addEventListener("pagehide", () => fields.forEach(field => field.close()), {signal: lifetime.signal});
document.addEventListener("htmx:beforeCleanupElement", event => {
	if (event.target === form || event.target.contains?.(form)) lifetime.abort();
}, {signal: lifetime.signal});
}
// @license-end
