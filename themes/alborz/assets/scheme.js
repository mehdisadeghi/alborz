// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// The scheme a reader chose against the system's, applied before the
// first paint so the page never shows the other one first. It lasts as
// long as the tab (ADR 50); without it the root says "system", which
// is also what shows the toggle, since the toggle needs this script.
document.documentElement.dataset.theme = sessionStorage.getItem("scheme") || "system";

// @license-end
