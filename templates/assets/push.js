// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// The browser subscribes, then the form carries the subscription to the server like any other form.
{
	const form = document.getElementById("push-form");
	const says = document.getElementById("script-failed");
	const fail = text => {
		says.querySelector(".notice-text").textContent = text;
		says.show();
	};
	const toBuffer = text => Uint8Array.from(atob(text.replace(/-/g, "+").replace(/_/g, "/")), c => c.charCodeAt(0));
	form.addEventListener("submit", async event => {
		if (form.elements.endpoint.value) {
			return;
		}
		event.preventDefault();
		if (!("serviceWorker" in navigator) || !("PushManager" in window) || !("Notification" in window)) {
			fail(form.dataset.unsupported);
			return;
		}
		if (await Notification.requestPermission() !== "granted") {
			fail(form.dataset.denied);
			return;
		}
		says.close();
		let subscription;
		try {
			const registration = await navigator.serviceWorker.ready;
			subscription = (await registration.pushManager.subscribe({
				userVisibleOnly: true,
				applicationServerKey: toBuffer(form.dataset.key),
			})).toJSON();
		} catch (e) {
			fail(form.dataset.failed);
			return;
		}
		form.elements.endpoint.value = subscription.endpoint;
		form.elements.p256dh.value = subscription.keys.p256dh;
		form.elements.auth.value = subscription.keys.auth;
		form.submit();
	});
}
// @license-end
