// Browser scripts are classic scripts sharing one global scope, so the
// globals they rely on are listed here rather than imported from npm.
const readonly = names => Object.fromEntries(names.map(n => [n, "readonly"]));

const browser = readonly([
	"AbortController", "BroadcastChannel", "CSS", "CSSRule", "CSSStyleSheet",
	"ClipboardEvent", "CustomEvent", "DOMException", "DOMParser", "DataTransfer",
	"DeviceOrientationEvent", "DragEvent", "Element", "Event",
	"EventSource", "File", "FocusEvent", "FormData", "HTMLElement", "HTMLFormElement",
	"HTMLInputElement", "Headers", "InputEvent", "IntersectionObserver", "KeyboardEvent",
	"MouseEvent", "MutationObserver", "Node", "Notification", "Path2D", "PointerEvent",
	"PublicKeyCredential", "Range", "Request", "ResizeObserver", "Response",
	"SubmitEvent", "TextEncoder", "URL", "URLSearchParams", "XMLHttpRequest",
	"addEventListener", "alert", "atob", "btoa", "cancelAnimationFrame", "caches",
	"clearInterval", "clearTimeout", "confirm", "console", "crypto", "document",
	"fetch", "getComputedStyle", "getSelection", "history", "indexedDB", "innerHeight",
	"innerWidth", "localStorage", "location", "matchMedia", "navigator", "open",
	"performance", "print", "prompt", "queueMicrotask", "removeEventListener",
	"requestAnimationFrame", "screen", "scrollTo", "sessionStorage", "setInterval", "setTimeout",
	"structuredClone", "visualViewport", "window",
	// Vendored libraries loaded before our scripts.
	"htmx", "Squire",
]);

const worker = readonly(["clients", "importScripts", "registration", "self"]);

const node = readonly([
	"AbortController", "Buffer", "Event", "EventTarget", "Headers", "Response",
	"URL", "URLSearchParams", "__dirname", "__filename", "clearInterval",
	"clearTimeout", "console", "module", "performance", "process", "require",
	"setImmediate", "setInterval", "setTimeout", "structuredClone",
]);

const rules = {
	// A leading underscore marks a parameter kept for its position.
	"no-unused-vars": ["warn", { argsIgnorePattern: "^_" }],
	"no-undef": "warn",
	// An empty catch deliberately ignores a best-effort failure.
	"no-empty": ["warn", { allowEmptyCatch: true }],
	"no-useless-escape": "warn",
	"no-constant-condition": "warn",
	"no-unreachable": "error",
	"no-dupe-keys": "error",
	"no-redeclare": "error",
};

export default [
	{ ignores: ["templates/assets/htmx.js", "templates/assets/squire.js", "local/", ".local/", "dist/"] },
	{
		files: ["templates/assets/**/*.js", "addons/*/web/**/*.js"],
		languageOptions: { ecmaVersion: "latest", sourceType: "script", globals: browser },
		rules,
	},
	{
		files: ["addons/season/web/kinds/*.js"],
		languageOptions: { ecmaVersion: "latest", sourceType: "module", globals: browser },
		rules,
	},
	{
		files: ["web/serviceworker.js"],
		languageOptions: { ecmaVersion: "latest", sourceType: "script", globals: { ...browser, ...worker } },
		rules,
	},
	{
		files: ["tests/**/*.cjs"],
		languageOptions: { ecmaVersion: "latest", sourceType: "commonjs", globals: node },
		rules,
	},
];
