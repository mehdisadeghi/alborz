// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// A block, for the reason compose.js is one.
{
const print = document.getElementById("print");
const frame = document.getElementById("email-frame");
const lifetime = new AbortController();
let printBody;

// The screen sandbox has no shadow host or document-wide page to style.
// Keep that boundary when its sanitized stylesheet is used for printing.
const printRules = rules => Array.from(rules, rule => {
	if (rule.type === CSSRule.STYLE_RULE) {
		const selector = rule.selectorText.replace(/\\(?:([0-9a-f]{1,6})\s?|([^\r\n\f]))/gi, (_, hex, char) => {
			const point = parseInt(hex, 16);
			return hex ? String.fromCodePoint(point > 0 && point <= 0x10ffff ? point : 0xfffd) : char;
		}).toLowerCase();
		return selector.includes(":host") || selector.includes("::slotted") ? "" : rule.cssText;
	}
	if (rule.type === CSSRule.MEDIA_RULE || rule.type === CSSRule.SUPPORTS_RULE) {
		const name = rule.type === CSSRule.MEDIA_RULE ? "media" : "supports";
		return "@" + name + " " + rule.conditionText + " {" + printRules(rule.cssRules) + "}";
	}
	return "";
}).join("\n");

// A screen-sized iframe clips text when paper reflows it. Put an already
// sanitized copy in normal page flow, isolating the sender's CSS in a shadow
// tree. The original frame, including its blocked resources, stays unchanged.
window.addEventListener("beforeprint", () => {
	if (printBody || !frame?.contentDocument?.body) return;
	printBody = document.createElement("div");
	printBody.id = "email-print";
	printBody.style.display = "none";
	const shadow = printBody.attachShadow({mode: "closed"});
	const style = document.createElement("style");
	// Shadow DOM isolates selectors, not painting. Contain negative margins and
	// shadows within the body's auto-sized box, away from trusted mail headers.
	style.textContent = ":host { all: initial; display: flow-root; contain: paint; width: fit-content; min-width: 100%; color: black; background: white; }";
	const body = frame.contentDocument.documentElement.cloneNode(true);
	for (const style of body.querySelectorAll("style")) {
		const sheet = new CSSStyleSheet();
		sheet.replaceSync(style.textContent);
		style.textContent = printRules(sheet.cssRules);
	}
	shadow.append(style, body);
	frame.after(printBody);
}, {signal: lifetime.signal});
const restore = () => {
	printBody?.remove();
	printBody = undefined;
};
window.addEventListener("afterprint", restore, {signal: lifetime.signal});
document.addEventListener("htmx:beforeCleanupElement", event => {
	if (event.detail.elt.contains(print)) {
		restore();
		lifetime.abort();
	}
}, {signal: lifetime.signal});
print.addEventListener("click", e => {
	e.preventDefault();
	window.print();
});
}

// @license-end
