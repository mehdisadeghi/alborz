// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat
"use strict";

// The emoji picker. The panel and its group headings are the page's; the emoji are read from
// emoji.txt when the panel first opens. A choice leaves as an "emoji" event carrying the
// character, for whoever holds the caret (editor.js).
// A block of its own: a boosted visit reruns this script.
{
const panel = document.getElementById("menu-emoji");
if (panel) {
	const search = panel.querySelector("input");
	const box = panel.querySelector(".emoji-groups");
	const none = panel.querySelector(".hint");
	const groups = [...panel.querySelectorAll("[data-group]")];
	const recent = groups.find(group => group.dataset.group === "recent");
	const gridOf = group => group.lastElementChild;
	const recentKey = "alborz.emoji", recentMax = 16;
	// Emoji to its name and the text a search reads; null until the list is read.
	let known = null, reading = null;

	const button = ch => {
		const b = document.createElement("button");
		b.type = "button";
		b.textContent = ch;
		b.title = known.get(ch).name;
		b.setAttribute("aria-label", b.title);
		return b;
	};

	// A line holds an emoji, its name and its keywords between tabs; "@" opens a group; the header has no tab.
	const read = () => reading ??= fetch(panel.dataset.src)
		.then(resp => resp.ok ? resp.text() : Promise.reject(new Error(resp.statusText)))
		.then(text => {
			known = new Map();
			let group = null;
			for (const line of text.split("\n")) {
				const [ch, name, words = ""] = line.split("\t");
				if (name === undefined) {
					group = ch[0] === "@" ? groups.find(g => g.dataset.group === ch.slice(1)) : group;
				} else if (group) {
					known.set(ch, { name: name, words: (name + " " + words).toLowerCase() });
					gridOf(group).append(button(ch));
				}
			}
		})
		.catch(err => {
			reading = null;
			throw err;
		});

	const stored = () => {
		try {
			const list = JSON.parse(localStorage.getItem(recentKey));
			return Array.isArray(list) ? list : [];
		} catch {
			return [];
		}
	};

	// Every word typed is in the name or the keywords. A search skips the recent ones: each is in its group too.
	const filter = () => {
		const terms = search.value.toLowerCase().split(/\s+/).filter(Boolean);
		let any = false;
		for (const group of groups) {
			let shown = false;
			for (const b of gridOf(group).children) {
				b.hidden = !terms.every(term => known.get(b.textContent).words.includes(term));
				shown ||= !b.hidden;
			}
			group.hidden = !shown || (group === recent && terms.length > 0);
			any ||= !group.hidden;
		}
		none.hidden = any;
	};
	search.addEventListener("input", () => known && filter());

	panel.addEventListener("toggle", ev => {
		if (ev.newState !== "open") {
			return;
		}
		search.value = "";
		read().then(() => {
			gridOf(recent).replaceChildren(...stored().filter(ch => known.has(ch)).map(button));
			filter();
			box.scrollTop = 0;
		}, () => {
			// As a request of the page's own that got no answer (helpers.js).
			const text = failedNotice().querySelector(".notice-text");
			showScriptFailure(text.dataset.noanswer ?? text.textContent);
		});
		// A touch screen would raise its keyboard over the grid.
		if (!matchMedia("(pointer: coarse)").matches) {
			search.focus();
		}
	});

	const pick = b => {
		const ch = b.textContent;
		try {
			localStorage.setItem(recentKey, JSON.stringify([ch, ...stored().filter(c => c !== ch)].slice(0, recentMax)));
		} catch {}
		panel.hidePopover();
		panel.dispatchEvent(new CustomEvent("emoji", { bubbles: true, detail: ch }));
	};
	panel.addEventListener("click", ev => {
		if (ev.target.tagName === "BUTTON") {
			pick(ev.target);
		}
	});

	const shown = () => groups.filter(group => !group.hidden).flatMap(group => [...gridOf(group).children].filter(b => !b.hidden));
	// The emoji nearest in the row above or below. Rows are found by position: a group ends mid-row.
	const inRow = (all, at, step) => {
		let best = null;
		for (let i = at + step; all[i]; i += step) {
			if (all[i].offsetTop === all[at].offsetTop) {
				continue;
			}
			if (best && all[i].offsetTop !== best.offsetTop) {
				break;
			}
			if (!best || Math.abs(all[i].offsetLeft - all[at].offsetLeft) < Math.abs(best.offsetLeft - all[at].offsetLeft)) {
				best = all[i];
			}
		}
		return best;
	};
	// Arrows walk the grid, from the search field and back to it; Enter and Space press the button they are on.
	panel.addEventListener("keydown", ev => {
		const all = shown();
		const at = all.indexOf(ev.target);
		const next = document.documentElement.dir === "rtl" ? -1 : 1;
		let to = null;
		if (ev.target === search) {
			// Enter in a field would send the message; here it takes the first emoji found.
			if (ev.key === "Enter" && !ev.isComposing) {
				ev.preventDefault();
				if (search.value.trim() && all[0]) {
					pick(all[0]);
				}
				return;
			}
			if (ev.key !== "ArrowDown") {
				return;
			}
			to = all[0];
		} else if (at < 0) {
			return;
		} else if (ev.key === "ArrowRight") {
			to = all[at + next];
		} else if (ev.key === "ArrowLeft") {
			to = all[at - next];
		} else if (ev.key === "ArrowDown") {
			to = inRow(all, at, 1);
		} else if (ev.key === "ArrowUp") {
			to = inRow(all, at, -1) || search;
		} else {
			return;
		}
		ev.preventDefault();
		if (to) {
			to.focus();
		}
	});
}
}

// @license-end
