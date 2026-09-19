// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// A passkey ceremony is two posts with the browser's prompt between
// them: the server says what to ask, the authenticator answers, the
// server checks the answer. WebAuthn speaks in buffers and JSON speaks
// in text, so both ways pass through base64url.
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

	// source carries where to begin and finish and what to say: the
	// unlock button, or the form that adds a passkey under a name.
	const ceremony = async (source, create, name) => {
		const says = document.getElementById("passkey-error");
		const fail = text => {
			says.textContent = text;
			says.hidden = false;
		};
		if (!window.PublicKeyCredential) {
			fail(source.dataset.unsupported);
			return;
		}
		says.hidden = true;
		try {
			const begun = await post(source.dataset.begin);
			if (!begun.ok) {
				throw new Error(begun.statusText);
			}
			const options = decode(await begun.json());
			const credential = create
				? await navigator.credentials.create(options)
				: await navigator.credentials.get(options);
			let finish = source.dataset.finish;
			if (name) {
				finish += "?name=" + encodeURIComponent(name);
			}
			const done = await post(finish, encode(credential));
			if (!done.ok) {
				throw new Error(done.statusText);
			}
			location.assign(source.dataset.next);
		} catch (err) {
			fail(source.dataset.failed);
		}
	};

	const unlock = document.getElementById("passkey-unlock");
	if (unlock) {
		unlock.addEventListener("click", () => ceremony(unlock, false));
	}
	const form = document.getElementById("passkey-form");
	if (form) {
		form.addEventListener("submit", event => {
			event.preventDefault();
			ceremony(form, true, form.elements.name.value.trim());
		});
	}
}

// @license-end
