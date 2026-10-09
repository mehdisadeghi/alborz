(() => {
const emailFrame = document.getElementById("email-frame");
if (emailFrame) {
	const frameStyle = document.getElementById("email-frame-style");
	let styleReady = !frameStyle || !!frameStyle.sheet;
	if (!styleReady) {
		const ready = () => { styleReady = true; };
		frameStyle.addEventListener("load", ready, { once: true });
		frameStyle.addEventListener("error", ready, { once: true });
	}
	// Size the frame to its content, scaling a too-wide mail down whole.
	// The frame arrives hidden and is shown once sized, so the unsized box never paints.
	let shown = false;
	const show = function() {
		if (!shown) {
			shown = true;
			emailFrame.style.visibility = "";
		}
	};
	// Without a doctype the size may live on body, so take the larger of both.
	// Offset sizes follow the frame, not the content, so only scroll sizes are read.
	const contentSize = function() {
		var doc = emailFrame.contentWindow.document;
		var root = doc.documentElement, body = doc.body;
		// Body margins are outside its scroll width; without them the frame is too narrow.
		var sides = 0;
		if (body) {
			var style = emailFrame.contentWindow.getComputedStyle(body);
			sides = parseFloat(style.marginLeft) + parseFloat(style.marginRight);
		}
		return {
			width: Math.max(root.scrollWidth, body ? body.scrollWidth + sides : 0),
			height: Math.max(root.scrollHeight, body ? body.scrollHeight : 0)
		};
	};
	// How much of the mail the frame cannot show at its current height.
	const unseen = function() {
		var doc = emailFrame.contentWindow.document;
		var root = doc.documentElement;
		if (!root || !root.clientHeight) {
			return 0;
		}
		return Math.max(0, Math.ceil(contentSize().height) - root.clientHeight);
	};

	// Grow the frame until the document shows all of it, in at most three passes.
	const fitFrame = function(scale) {
		for (var pass = 0; pass < 3; pass++) {
			var height = Math.ceil(contentSize().height) +
				Math.max(0, emailFrame.offsetHeight - emailFrame.clientHeight);
			if (pass > 0) {
				height = parseFloat(emailFrame.style.height) + unseen();
			}
			emailFrame.style.height = height + "px";
			if (scale) {
				emailFrame.parentNode.style.height = (height * scale) + "px";
			}
			if (unseen() === 0) {
				break;
			}
		}
	};

	let applied = { width: -1, height: -1, avail: -1 };
	// The height this script last wrote; one set by anyone else is left alone.
	let ours = "";
	const resizeFrame = function(force) {
		try {
			if (!styleReady) {
				return;
			}
			if (!force && emailFrame.style.height !== ours) {
				return;
			}
			var avail = emailFrame.parentNode.clientWidth;
			var size = contentSize();
			// A relayout collapses the frame and yanks the scroll to the top, so only act on change.
			if (!force && avail === applied.avail &&
				Math.abs(size.height - applied.height) <= 2 &&
				Math.abs(size.width - applied.width) <= 2) {
				return;
			}
			emailFrame.style.width = "";
			emailFrame.style.maxWidth = "";
			emailFrame.style.transform = "";
			// A scroll size is at least the viewport, so collapse the frame to measure the content.
			emailFrame.style.height = "0px";
			avail = emailFrame.clientWidth;
			var width = contentSize().width;
			if (width > avail) {
				var f = avail / width;
				// The theme clamps the frame to its column; the scaled-down frame must escape that.
				emailFrame.style.maxWidth = "none";
				emailFrame.style.width = width + "px";
				emailFrame.style.transformOrigin = "0 0";
				emailFrame.style.transform = "scale(" + f + ")";
				fitFrame(f);
			} else {
				emailFrame.parentNode.style.height = "";
				fitFrame(0);
			}
			ours = emailFrame.style.height;
			var settled = contentSize();
			applied = { width: settled.width, height: settled.height,
				avail: emailFrame.parentNode.clientWidth };
			show();
		} catch (e) {
			window.alborzFrameError = String(e);
			show();
		}
	};
	// Keep the frame sized to its content, so its scrollbar never competes with the page's.
	const lockFrame = function() {
		try {
			var win = emailFrame.contentWindow;
			var doc = win.document;
			if (doc.alborzLocked) {
				return;
			}
			doc.alborzLocked = true;
			win.addEventListener("resize", function() {
				resizeFrame(false);
			});
			if (win.ResizeObserver) {
				var ro = new win.ResizeObserver(function() {
					resizeFrame(false);
				});
				if (doc.documentElement) {
					ro.observe(doc.documentElement);
				}
				if (doc.body) {
					ro.observe(doc.body);
				}
			}
		} catch (e) {
			window.alborzFrameError = String(e);
		}
	};
	emailFrame.addEventListener("load", function() {
		lockFrame();
		resizeFrame(true);
	});
	window.addEventListener("resize", function() {
		resizeFrame(true);
	});
	// load may fire before this script attaches, so poll for the completed document.
	let bootTicks = 0;
	const boot = setInterval(function() {
		bootTicks++;
		var doc = emailFrame.contentDocument;
		if (styleReady && doc && doc.readyState === "complete" && doc.body && doc.body.childNodes.length > 0) {
			lockFrame();
			resizeFrame(true);
		}
		if (applied.height > 50 || bootTicks > 30) {
			clearInterval(boot);
			show();
		}
	}, 150);

	// Polyfill in case the srcdoc attribute isn't supported
	if (!emailFrame.srcdoc) {
		var srcdoc = emailFrame.getAttribute("srcdoc");
		var doc = emailFrame.contentWindow.document;
		doc.open();
		doc.write(srcdoc);
		doc.close();
	}
}
})();
