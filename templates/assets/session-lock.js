// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat
(() => {
	const meta = document.querySelector('meta[name="alborz-session"]');
	if (!meta) { return; }
	const id = meta.content, data = meta.dataset;
	const lockKey = "alborz-locked:" + id, enabledKey = "alborz-protected:" + id;
	const read = key => { try { return localStorage.getItem(key); } catch { return null; } };
	const write = (key, value) => { try { value === null ? localStorage.removeItem(key) : localStorage.setItem(key, value); } catch {} };
	const unlockPage = location.pathname === data.unlock;
	const privatePage = data.private === "true" && !unlockPage;
	let after = Number(data.after) * 1000;
	let protectedPage = after > 0 || read(enabledKey) === "1" || !!read(lockKey);
	let generation = 0, timer, told = Date.now(), pendingLock = false;
	let historyRequest = null, historyFailed = false;
	const cancelHistory = () => {
		const request = historyRequest;
		historyRequest = null;
		request?.abort?.();
	};
	let title = document.title, message = data.checking;
	let channel;
	try { if (typeof BroadcastChannel !== "undefined") { channel = new BroadcastChannel("alborz-session:" + id); } } catch {}
	const broadcast = type => channel?.postMessage({session: id, type});
	const paint = () => {
		const status = document.getElementById("session-lock-status");
		if (status) { status.textContent = message; }
	};
	const conceal = text => {
		if (!privatePage) { return; }
		if (!document.documentElement.hasAttribute("data-session-concealed")) { title = document.title; }
		message = text || data.checking;
		document.documentElement.setAttribute("data-session-concealed", "");
		document.title = data.title;
		paint();
	};
	const leave = () => {
		if (privatePage) { location.assign(data.unlock + "?" + new URLSearchParams({next: location.pathname + location.search})); }
	};
	const confirmedLock = (announce = true) => {
		cancelHistory();
		historyFailed = false;
		generation++;
		protectedPage = true;
		pendingLock = false;
		clearTimeout(timer);
		conceal(data.title);
		if (announce) {
			write(enabledKey, "1");
			write(lockKey, Date.now() + ":" + Math.random());
			broadcast("locked");
		}
		leave();
	};
	const arm = () => {
		clearTimeout(timer);
		if (privatePage && protectedPage && after > 0 && !pendingLock) {
			// Another tab can keep the server active: expiry asks, it never assumes a lock.
			timer = setTimeout(() => verify(), after);
		}
	};
	const verify = async () => {
		if (!privatePage) { return; }
		// A successful lock check cannot validate stale content at a failed history URL.
		if (historyRequest || historyFailed) { conceal(historyFailed ? data.loadfailed : data.checking); return; }
		const ticket = ++generation, marker = read(lockKey);
		conceal();
		try {
			const response = await fetch(data.state, {headers: {"Alborz-Unasked": "1"}, credentials: "same-origin", cache: "no-store", redirect: "manual"});
			if (!response.ok) { leave(); throw new Error("session unavailable"); }
			const state = await response.json();
			if (ticket !== generation || marker !== read(lockKey)) { return; }
			if (state.session !== id) { leave(); return; }
			if (state.locked) { confirmedLock(); return; }
			protectedPage = state.protected;
			after = Number(state.after) * 1000;
			write(enabledKey, protectedPage ? "1" : null);
			// A successful server check may reveal, but must never clear a lock marker:
			// only the WebAuthn finish response authorizes that cross-tab change.
			if (pendingLock) {
				if (!state.protected) { location.assign(data.setup); }
				conceal(data.failed);
				return;
			}
			if (document.visibilityState !== "hidden") {
				document.documentElement.removeAttribute("data-session-concealed");
				document.title = title;
			}
			arm();
		} catch {
			if (ticket === generation) { conceal(data.failed); }
		}
	};
	const manualLock = async () => {
		cancelHistory();
		historyFailed = false;
		generation++;
		pendingLock = true;
		conceal();
		try {
			const response = await fetch(data.lock, {method: "POST", headers: {"Alborz-Lock": "1"}, credentials: "same-origin", redirect: "manual"});
			if (response.status === 204) { confirmedLock(); return; }
		} catch {}
		// The server may have accepted a POST whose reply was lost. Verify before
		// claiming success; an unreachable or still-open session stays concealed here.
		await verify();
	};
	const changed = type => {
		if (type === "locked") { confirmedLock(false); }
		if (type === "unlocked" || type === "protected") {
			generation++;
			protectedPage = true;
			pendingLock = false;
			verify();
		}
	};
	if (channel) { channel.onmessage = event => { if (event.data?.session === id) { changed(event.data.type); } }; }
	window.addEventListener("storage", event => {
		if (event.key === lockKey) { changed(event.newValue ? "locked" : "unlocked"); }
		if (event.key === enabledKey && event.newValue === "1") { changed("protected"); }
	});
	window.AlborzLock = {
		lock: manualLock, verify, locked: () => confirmedLock(), unlockPage, privatePage,
		unlocked() {
			generation++;
			write(lockKey, null);
			broadcast("unlocked");
		},
		registered() {
			write(enabledKey, "1");
			broadcast("protected");
		},
	};
	if (privatePage && protectedPage) { conceal(); }
	document.addEventListener("DOMContentLoaded", () => {
		paint();
		if (privatePage && protectedPage) { verify(); }
	});
	document.addEventListener("click", event => {
		if (!event.target.closest?.("#session-lock-retry")) { return; }
		event.preventDefault();
		if (historyRequest || historyFailed) { location.reload(); }
		else if (pendingLock) { manualLock(); }
		else { verify(); }
	});
	document.addEventListener("submit", event => {
		if (event.target.matches?.("form[data-session-lock]")) { event.preventDefault(); manualLock(); }
	});
	const hide = () => { if (privatePage && protectedPage) { generation++; clearTimeout(timer); conceal(); } };
	window.addEventListener("pagehide", hide);
	window.addEventListener("pageshow", event => { if (event.persisted && protectedPage) { verify(); } });
	let historyRedirect = false;
	const historyFailure = xhr => {
		if (!xhr || xhr !== historyRequest) { return; }
		historyRequest = null;
		historyFailed = true;
		generation++;
		clearTimeout(timer);
		conceal(data.loadfailed);
	};
	document.addEventListener("htmx:historyCacheMiss", event => {
		if (!privatePage || !protectedPage) { return; }
		// A Back from an open, visible page stays shown; the server's answer enforces a lock.
		const trusted = canAct();
		cancelHistory();
		historyRedirect = false;
		historyFailed = false;
		historyRequest = event.detail?.xhr || {};
		if (!trusted) { hide(); }
		const xhr = historyRequest;
		xhr.timeout = 20000;
		// The bundled history loader does not report network failures or aborts.
		for (const type of ["error", "timeout", "abort"]) {
			xhr.addEventListener?.(type, () => historyFailure(xhr), {once: true});
		}
	});
	document.addEventListener("htmx:historyCacheMissLoadError", event => historyFailure(event.detail?.xhr));
	for (const type of ["htmx:historyCacheHit", "htmx:beforeHistoryUpdate"]) {
		document.addEventListener(type, () => { cancelHistory(); historyFailed = false; });
	}
	document.addEventListener("htmx:historyRestore", () => {
		const concealed = document.documentElement.hasAttribute("data-session-concealed");
		historyRequest = null;
		historyFailed = false;
		if (!historyRedirect && privatePage && protectedPage && concealed) { verify(); }
	});
	// htmx's history loader does not process HX-Redirect. Keep its swap detached
	// while the unlock page loads with its own session guard and document head.
	document.addEventListener("htmx:historyCacheMissLoad", event => {
		const target = event.detail?.xhr?.getResponseHeader("HX-Redirect");
		if (!target) { return; }
		historyRedirect = true;
		generation++;
		clearTimeout(timer);
		conceal(data.title);
		event.detail.historyElt = document.createElement("body");
		location.assign(target);
	});
	document.addEventListener("visibilitychange", () => {
		if (document.visibilityState === "hidden") { hide(); }
		else if (privatePage && protectedPage) { verify(); }
	});
	document.addEventListener("alborz:streamclosed", () => { if (privatePage && protectedPage) { verify(); } });
	const canAct = () => privatePage && protectedPage && !pendingLock && document.visibilityState !== "hidden" &&
		!document.documentElement.hasAttribute("data-session-concealed");
	const activity = () => {
		if (!canAct()) { return; }
		arm();
		if (Date.now() - told < Math.min(60000, after / 2)) { return; }
		told = Date.now();
		fetch(data.alive, {method: "POST", credentials: "same-origin", redirect: "manual"}).then(response => {
			if (response.status !== 204) { verify(); }
		}).catch(() => verify());
	};
	let lastInput = -Infinity;
	const watchActivity = (target, current = () => true) => {
		const input = event => {
			if (!current() || !event.isTrusted || !canAct()) { return; }
			if (event.type === "scroll") {
				// Browser-generated scroll events also follow scripts and layout changes.
				// Count only the short tail of real input, including touch momentum.
				if (Date.now() - lastInput > 1500) { return; }
			} else {
				if (event.type === "pointermove" && !event.buttons) { return; }
				lastInput = Date.now();
			}
			activity();
		};
		for (const type of ["pointerdown", "pointermove", "keydown", "wheel", "touchmove", "scroll"]) {
			target.addEventListener(type, input, {passive: true, capture: true});
		}
	};
	watchActivity(document);
	const watchedFrames = new WeakSet();
	const watchEmail = () => {
		const frame = document.getElementById("email-frame");
		if (!frame?.hasAttribute("srcdoc") || !frame.sandbox.contains("allow-same-origin") || frame.sandbox.contains("allow-scripts")) { return; }
		const doc = frame.contentDocument;
		if (!doc || watchedFrames.has(doc)) { return; }
		watchedFrames.add(doc);
		// Sender scripts stay disabled. Read genuine events directly, without a message bridge.
		watchActivity(doc, () => frame.isConnected && frame.contentDocument === doc);
	};
	document.addEventListener("load", event => {
		if (event.target === document.getElementById("email-frame")) { watchEmail(); }
	}, true);
	for (const type of ["DOMContentLoaded", "htmx:load"]) { document.addEventListener(type, watchEmail); }
	document.addEventListener("htmx:afterRequest", event => {
		if (!event.detail?.requestConfig?.headers?.["Alborz-Unasked"]) { activity(); }
	});
})();
// @license-end
