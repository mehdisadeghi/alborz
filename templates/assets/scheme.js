// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// Styles load after this script. Until then the server's metadata supplies the colour.
let schemeStylesReady = document.readyState !== "loading";
const syncThemeColor = () => {
	if (!schemeStylesReady) {
		return;
	}
	const color = getComputedStyle(document.documentElement).getPropertyValue("--theme-color").trim();
	if (!color) {
		return;
	}
	// One unconditional colour follows the actual cascade, including pinned themes and custom CSS.
	for (const [i, meta] of [...document.querySelectorAll('meta[name="theme-color"]')].entries()) {
		meta.media = i === 0 ? "all" : "not all";
		if (i === 0) {
			meta.content = color;
		}
	}
};

const applyScheme = scheme => {
	document.documentElement.dataset.theme = scheme;
	for (const meta of document.querySelectorAll('meta[name="theme-color"][data-scheme]')) {
		meta.media = scheme === "system" ? `(prefers-color-scheme: ${meta.dataset.scheme})` : meta.dataset.scheme === scheme ? "all" : "not all";
	}
	syncThemeColor();
};

// Applied before the first paint so the other scheme never shows first (ADR 50).
applyScheme(sessionStorage.getItem("scheme") || "system");

document.addEventListener("DOMContentLoaded", () => {
	schemeStylesReady = true;
	syncThemeColor();
});
document.addEventListener("htmx:afterSwap", syncThemeColor);
matchMedia("(prefers-color-scheme: dark)").addEventListener("change", syncThemeColor);

// @license-end
