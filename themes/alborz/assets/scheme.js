// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// Applied before the first paint so the other scheme never shows first (ADR 50).
document.documentElement.dataset.theme = sessionStorage.getItem("scheme") || "system";

// @license-end
