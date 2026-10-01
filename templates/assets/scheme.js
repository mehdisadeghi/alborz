// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// The status bar follows an override too, in the colours the page was served with.
const applyScheme = scheme => {
	document.documentElement.dataset.theme = scheme;
	for (const meta of document.querySelectorAll('meta[name="theme-color"][data-scheme]')) {
		meta.media = scheme === "system" ? `(prefers-color-scheme: ${meta.dataset.scheme})` : meta.dataset.scheme === scheme ? "all" : "not all";
	}
};

// Applied before the first paint so the other scheme never shows first (ADR 50).
applyScheme(sessionStorage.getItem("scheme") || "system");

// @license-end
