// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

(() => {
	const root = document.documentElement;
	const viewport = window.visualViewport;
	let width = 0, visualWidth = 0, baseline = 0, pending = false;
	const editing = element => element && !element.disabled && !element.readOnly &&
		(element.isContentEditable || element.tagName === "TEXTAREA" ||
			element.tagName === "INPUT" && /^(text|search|email|url|tel|password|number)$/.test(element.type));
	const clear = () => {
		delete root.dataset.phoneKeyboard;
		root.style.removeProperty("--phone-viewport-height");
	};
	const sync = () => {
		pending = false;
		const nextWidth = root.clientWidth || window.innerWidth;
		if (!viewport || root.dataset.phone !== "bar" || nextWidth > 600) {
			baseline = width = visualWidth = 0;
			clear();
			return;
		}
		// Pinch zoom also shrinks VisualViewport; it is not keyboard occlusion.
		if (Math.abs(viewport.scale - 1) > 0.01) {
			clear();
			return;
		}
		const height = Math.max(window.innerHeight, root.clientHeight, viewport.height);
		if (nextWidth !== width || viewport.width !== visualWidth) {
			width = nextWidth;
			visualWidth = viewport.width;
			baseline = height;
		}
		const focused = editing(document.activeElement);
		// Android can resize both viewports. Retain the largest height for this
		// width, including recovery when a keyboard closes without blurring.
		baseline = Math.max(baseline, height);
		root.style.setProperty("--phone-viewport-height", viewport.height + "px");
		// There is no universal keyboard API. Require substantial height loss as
		// well as an editor, so hardware keyboards and toolbars leave the bar usable.
		if (focused && baseline - viewport.height >= Math.max(150, baseline * 0.25)) {
			root.dataset.phoneKeyboard = "open";
		} else {
			delete root.dataset.phoneKeyboard;
		}
	};
	const schedule = () => {
		if (pending) return;
		pending = true;
		requestAnimationFrame(sync);
	};
	window.addEventListener("resize", schedule);
	window.addEventListener("orientationchange", () => {
		baseline = width = visualWidth = 0;
		schedule();
	});
	viewport?.addEventListener("resize", schedule);
	document.addEventListener("focusin", schedule);
	document.addEventListener("focusout", schedule);
	document.addEventListener("htmx:afterSwap", schedule);
	sync();
})();

// @license-end
