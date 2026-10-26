// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// WebAuthn speaks in buffers and JSON in text, so both ways pass through base64url.
{
	const toBuffer = text => {
		const padded = text.replace(/-/g, "+").replace(/_/g, "/");
		return Uint8Array.from(atob(padded), c => c.charCodeAt(0)).buffer;
	};
	const toText = buffer => btoa(String.fromCharCode(...new Uint8Array(buffer)))
		.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

	const decode = options => {
		const key = options.publicKey;
		key.challenge = toBuffer(key.challenge);
		if (key.user) {
			key.user.id = toBuffer(key.user.id);
		}
		for (const list of [key.excludeCredentials, key.allowCredentials]) {
			for (const credential of list || []) {
				credential.id = toBuffer(credential.id);
			}
		}
		return options;
	};

	const encode = credential => {
		const response = {};
		for (const name of ["clientDataJSON", "attestationObject", "authenticatorData", "signature", "userHandle"]) {
			if (credential.response[name]) {
				response[name] = toText(credential.response[name]);
			}
		}
		return {
			id: credential.id,
			rawId: toText(credential.rawId),
			type: credential.type,
			response: response,
		};
	};

	const post = (url, body) => fetch(url, {
		method: "POST",
		headers: {"Content-Type": "application/json"},
		body: body ? JSON.stringify(body) : null,
	});

	let pending = false;
	const ceremony = async (source, create, automatic = false) => {
		if (pending) { return; }
		const says = document.getElementById("script-failed");
		const fail = text => showScriptFailure(text);
		if (!window.PublicKeyCredential) {
			if (!automatic) { fail(source.dataset.unsupported); }
			return;
		}
		pending = true;
		const button = create ? source.querySelector('button[type="submit"]') : source;
		if (button) { button.disabled = true; }
		says.close();
		try {
			const begun = await post(source.dataset.begin);
			if (!begun.ok) {
				throw new Error(begun.statusText);
			}
			const options = decode(await begun.json());
			const credential = create
				? await navigator.credentials.create(options)
				: await navigator.credentials.get(options);
			const done = await post(source.dataset.finish, encode(credential));
			if (!done.ok) {
				throw new Error(done.statusText);
			}
			if (create) { window.AlborzLock?.registered(); }
			else { window.AlborzLock?.unlocked(); }
			location.assign(source.dataset.next);
		} catch (err) {
			pending = false;
			if (button) { button.disabled = false; }
			// Cancellation or a browser requiring a tap leaves the unlock button available.
			if (!automatic || err.name !== "NotAllowedError") { fail(source.dataset.failed); }
		}
	};

	const unlock = document.getElementById("passkey-unlock");
	if (unlock) {
		let attempted = false;
		unlock.addEventListener("click", () => {
			attempted = true;
			return ceremony(unlock, false);
		});
		const automatic = () => {
			if (attempted || document.visibilityState !== "visible") { return; }
			attempted = true;
			ceremony(unlock, false, true);
		};
		// Once per document: native passkey dialogs can themselves hide and reveal it.
		// Wait for the deferred helpers before showing any failure notice.
		const ready = () => {
			automatic();
			document.addEventListener("visibilitychange", automatic);
		};
		if (document.readyState === "loading") {
			document.addEventListener("DOMContentLoaded", ready, {once: true});
		} else { ready(); }
	}
	const form = document.getElementById("passkey-form");
	if (form) {
		form.addEventListener("submit", event => {
			event.preventDefault();
			return ceremony(form, true);
		});
	}
	// A history snapshot can retain a completed ceremony and disabled controls.
	// Reload so the server checks the current session before offering another one.
	window.addEventListener("pageshow", event => {
		if (event.persisted) { location.reload(); }
	});
}

// @license-end
