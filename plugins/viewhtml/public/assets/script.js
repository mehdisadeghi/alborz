var emailFrame = document.getElementById("email-frame");
if (emailFrame) {
	// Size the frame to its content. A mail laid out wider than the
	// screen is scaled down whole, the way mobile clients render it,
	// instead of panning inside the frame.
	// Mails often lack a doctype, and in quirks mode the content size
	// lives on body instead of documentElement, differently per browser;
	// the maximum over both is right everywhere.
	var contentSize = function() {
		var doc = emailFrame.contentWindow.document;
		var root = doc.documentElement, body = doc.body;
		return {
			width: Math.max(root.scrollWidth, root.offsetWidth,
				body ? body.scrollWidth : 0, body ? body.offsetWidth : 0),
			height: Math.max(root.scrollHeight, root.offsetHeight,
				body ? body.scrollHeight : 0, body ? body.offsetHeight : 0)
		};
	};
	var applied = { width: -1, height: -1, avail: -1 };
	var resizeFrame = function(force) {
		try {
			var avail = emailFrame.parentNode.clientWidth;
			var size = contentSize();
			// Re-laying out on every observer tick collapses the frame
			// for a moment, and the shrunken page yanks the scroll
			// position to the top. Nothing is touched unless the
			// content actually changed.
			if (!force && avail === applied.avail &&
				Math.abs(size.height - applied.height) <= 2 &&
				Math.abs(size.width - applied.width) <= 2) {
				return;
			}
			emailFrame.style.width = "";
			emailFrame.style.maxWidth = "";
			emailFrame.style.transform = "";
			emailFrame.style.height = "";
			avail = emailFrame.clientWidth;
			var width = contentSize().width;
			if (width > avail) {
				var f = avail / width;
				// The theme clamps the frame to its column; the widened,
				// scaled-down frame must escape that.
				emailFrame.style.maxWidth = "none";
				emailFrame.style.width = width + "px";
				emailFrame.style.transformOrigin = "0 0";
				emailFrame.style.transform = "scale(" + f + ")";
				var height = contentSize().height;
				emailFrame.style.height = height + "px";
				emailFrame.parentNode.style.height = (height * f) + "px";
			} else {
				emailFrame.style.height = contentSize().height + "px";
				emailFrame.parentNode.style.height = "";
			}
			var settled = contentSize();
			applied = { width: settled.width, height: settled.height,
				avail: emailFrame.parentNode.clientWidth };
		} catch (e) {
			window.alborzFrameError = String(e);
		}
	};
	// The frame must never scroll on its own: its scrollbar competes
	// with the page's. The content document is clipped instead, and a
	// ResizeObserver keeps the frame sized to it as layout settles.
	var lockFrame = function() {
		try {
			var win = emailFrame.contentWindow;
			var doc = win.document;
			if (doc.alborzLocked) {
				return;
			}
			doc.alborzLocked = true;
			if (doc.documentElement) {
				doc.documentElement.style.overflow = "hidden";
			}
			if (doc.body) {
				doc.body.style.overflow = "hidden";
			}
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
	// The srcdoc document appears at a different moment per engine, and
	// the load event can fire before this script attaches; poll briefly
	// until the real content is sized.
	var bootTicks = 0;
	var boot = setInterval(function() {
		bootTicks++;
		var doc = emailFrame.contentDocument;
		if (doc && doc.body && doc.body.childNodes.length > 0) {
			lockFrame();
			resizeFrame(true);
		}
		if (applied.height > 50 || bootTicks > 30) {
			clearInterval(boot);
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
