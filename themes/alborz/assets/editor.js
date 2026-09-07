// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat
"use strict";

// The rich text editor on the compose page: Squire on a div beside the
// textarea, chosen per message. The textarea stays the baseline; a
// message written here goes out as HTML, and the direction of each
// paragraph is decided by the server from the text, since the browser's
// dir=auto is not something every receiving client understands.

// A block of its own: compose.js shares the page and its names.
{
const form = document.getElementById("compose-form");
const textarea = form && form.querySelector("textarea.body");
const holder = form && form.querySelector(".editor");
const toolbar = form && form.querySelector(".editor-toolbar");
const htmlInput = form && form.querySelector("input[name=html]");
const toggle = form && form.querySelector("[data-editor-toggle]");

if (form && textarea && holder && toolbar && htmlInput && toggle && window.Squire) {
	let editor = null;

	// What the editor may hold: the tags the server will keep. Anything
	// pasted from elsewhere is reduced to these before it enters.
	const allowed = { P: 1, DIV: 1, BR: 1, B: 1, STRONG: 1, I: 1, EM: 1, U: 1, S: 1, UL: 1, OL: 1, LI: 1, BLOCKQUOTE: 1, A: 1 };
	const sanitize = html => {
		const doc = new DOMParser().parseFromString(html, "text/html");
		const frag = document.createDocumentFragment();
		const copy = (from, to) => {
			for (const node of from.childNodes) {
				if (node.nodeType === Node.TEXT_NODE) {
					to.appendChild(document.createTextNode(node.data));
				} else if (node.nodeType === Node.ELEMENT_NODE) {
					if (node.tagName === "SCRIPT" || node.tagName === "STYLE") {
						continue;
					}
					if (!allowed[node.tagName]) {
						copy(node, to);
						continue;
					}
					const el = document.createElement(node.tagName);
					if (node.tagName === "A" && /^(https?:|mailto:)/i.test(node.getAttribute("href") || "")) {
						el.setAttribute("href", node.getAttribute("href"));
					}
					const dir = node.getAttribute("dir");
					if (dir === "ltr" || dir === "rtl" || (dir === "auto" && node.tagName !== "UL" && node.tagName !== "OL")) {
						el.setAttribute("dir", dir);
					}
					copy(node, el);
					to.appendChild(el);
				}
			}
		};
		copy(doc.body, frag);
		return frag;
	};

	const escape = s => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

	// Plain text to the editor's HTML: a blank line ends a paragraph and
	// a run of quoted lines is a quote, the same reading the server gives
	// text when it adds direction.
	const fromText = text => {
		const out = [];
		let para = [], quoted = false, inQuote = false;
		const flush = () => {
			if (para.length === 0) {
				return;
			}
			if (quoted && !inQuote) {
				out.push("<blockquote>");
				inQuote = true;
			} else if (!quoted && inQuote) {
				out.push("</blockquote>");
				inQuote = false;
			}
			out.push('<p dir="auto">' + para.map(escape).join("<br>") + "</p>");
			para = [];
		};
		for (const raw of text.replace(/\r\n/g, "\n").split("\n")) {
			const q = /^>\s?/.test(raw);
			const line = q ? raw.replace(/^>\s?/, "") : raw;
			if (line === "" || q !== quoted) {
				flush();
				quoted = q;
				if (line === "") {
					continue;
				}
			}
			para.push(line);
		}
		flush();
		if (inQuote) {
			out.push("</blockquote>");
		}
		return out.join("\n");
	};

	// The editor's HTML back to text, for the textarea when the editor is
	// put away: paragraphs by blank lines, quotes by their mark.
	const toText = root => {
		const lines = [];
		const walk = (node, prefix) => {
			for (const child of node.children) {
				if (child.tagName === "BLOCKQUOTE") {
					walk(child, prefix + "> ");
				} else if (child.tagName === "UL" || child.tagName === "OL") {
					let n = 0;
					for (const li of child.children) {
						n++;
						lines.push(prefix + (child.tagName === "OL" ? n + ". " : "- ") + li.innerText.trim());
					}
					lines.push(prefix.trimEnd());
				} else {
					lines.push(prefix + child.innerText.replace(/\n/g, "\n" + prefix));
					lines.push(prefix.trimEnd());
				}
			}
		};
		walk(root, "");
		return lines.join("\n").replace(/\n{3,}/g, "\n\n").trim() + "\n";
	};

	const open = html => {
		holder.hidden = false;
		toolbar.classList.add("is-on");
		textarea.hidden = true;
		editor = new Squire(holder, {
			blockTag: "P",
			blockAttributes: { dir: "auto" },
			sanitizeToDOMFragment: sanitize,
		});
		editor.setHTML(html);
		editor.addEventListener("pathChange", reflect);
		editor.addEventListener("select", reflect);
		editor.moveCursorToEnd();
		reflect();
		toggle.setAttribute("aria-pressed", "true");
	};

	const close = () => {
		textarea.value = toText(holder);
		editor.destroy();
		editor = null;
		holder.innerHTML = "";
		holder.hidden = true;
		toolbar.classList.remove("is-on");
		textarea.hidden = false;
		htmlInput.value = "";
		toggle.setAttribute("aria-pressed", "false");
	};

	// The buttons say which state the caret is in, so bold reads as on
	// inside bold text, the way every editor since Word does it.
	const reflect = () => {
		if (!editor) {
			return;
		}
		for (const button of toolbar.querySelectorAll("[data-act]")) {
			let on = false;
			switch (button.dataset.act) {
			case "bold": on = editor.hasFormat("B"); break;
			case "italic": on = editor.hasFormat("I"); break;
			case "list": on = editor.hasFormat("UL"); break;
			case "quote": on = editor.hasFormat("BLOCKQUOTE"); break;
			case "link": on = editor.hasFormat("A"); break;
			}
			button.setAttribute("aria-pressed", on ? "true" : "false");
		}
	};

	// Squire writes a direction on the block, which for a list item
	// leaves the list itself in the page's direction: the marker and the
	// indent then sit on the wrong side. The list follows its items.
	const listOfCaret = () => {
		const range = editor.getSelection();
		let node = range && range.startContainer;
		while (node && node !== holder) {
			if (node.tagName === "UL" || node.tagName === "OL") {
				return node;
			}
			node = node.parentNode;
		}
		return null;
	};
	const settleList = () => {
		const list = listOfCaret();
		if (!list) {
			return;
		}
		const item = list.querySelector("li[dir]");
		if (item) {
			list.setAttribute("dir", item.getAttribute("dir"));
		}
	};

	toolbar.addEventListener("click", ev => {
		const button = ev.target.closest("[data-act]");
		if (!button || !editor) {
			return;
		}
		ev.preventDefault();
		switch (button.dataset.act) {
		case "bold": editor.hasFormat("B") ? editor.removeBold() : editor.bold(); break;
		case "italic": editor.hasFormat("I") ? editor.removeItalic() : editor.italic(); break;
		case "list": editor.hasFormat("UL") ? editor.removeList() : editor.makeUnorderedList(); settleList(); break;
		case "quote": editor.hasFormat("BLOCKQUOTE") ? editor.decreaseQuoteLevel() : editor.increaseQuoteLevel(); break;
		case "link": {
			if (editor.hasFormat("A")) {
				editor.removeLink();
				break;
			}
			const url = window.prompt(button.dataset.prompt, editor.getSelectedText().trim());
			if (url) {
				editor.makeLink(url);
			}
			break;
		}
		case "rtl": editor.setTextDirection("rtl"); settleList(); break;
		case "ltr": editor.setTextDirection("ltr"); settleList(); break;
		case "clear": editor.removeAllFormatting(); break;
		}
		editor.focus();
		reflect();
	});

	// An untouched reply opens on the original's HTML, quoted; one the
	// writer has already typed into keeps what was typed.
	const quoteHTML = document.getElementById("quote-html");
	toolbar.hidden = false;
	toggle.addEventListener("click", ev => {
		ev.preventDefault();
		if (editor) {
			close();
		} else if (quoteHTML && textarea.value === textarea.defaultValue) {
			open(quoteHTML.innerHTML);
		} else {
			open(fromText(textarea.value));
		}
	});

	// Before any submit listener sends the form: the editor's HTML into
	// its field, and its text into the textarea so a client without HTML
	// and a reopened draft read the same words.
	form.addEventListener("submit", () => {
		if (editor) {
			htmlInput.value = editor.getHTML();
			textarea.value = toText(holder);
		}
	}, true);

	// A draft written here opens here.
	if (htmlInput.value) {
		open(htmlInput.value);
	}
}
}

// @license-end
