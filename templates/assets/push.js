// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// The browser subscribes, then the form carries the subscription to the server like any other form.
{
	const settings = document.getElementById("push-settings");
	const fail = text => showScriptFailure(text);
	const toBuffer = text => Uint8Array.from(atob(text.replace(/-/g, "+").replace(/_/g, "/")), c => c.charCodeAt(0));
	const subscribe = async event => {
		const form = event.currentTarget;
		if (form.elements.endpoint.value) {
			return;
		}
		event.preventDefault();
		if (!("serviceWorker" in navigator) || !("PushManager" in window) || !("Notification" in window)) {
			// Only iOS Safari has navigator.standalone, and it gives web push only to a Home Screen app.
			const outsideHomeScreen = "standalone" in navigator && !navigator.standalone && !matchMedia("(display-mode: standalone)").matches;
			fail(outsideHomeScreen ? settings.dataset.homescreen : settings.dataset.unsupported);
			return;
		}
		let subscription;
		try {
			if (Notification.permission !== "granted" && await Notification.requestPermission() !== "granted") {
				fail(settings.dataset.denied);
				return;
			}
			failedNotice().close();
			const registration = await navigator.serviceWorker.ready;
			const sub = await registration.pushManager.getSubscription() || await registration.pushManager.subscribe({
				userVisibleOnly: true,
				applicationServerKey: toBuffer(settings.dataset.key),
			});
			subscription = sub.toJSON();
		} catch (e) {
			fail(settings.dataset.failed);
			return;
		}
		form.elements.endpoint.value = subscription.endpoint;
		form.elements.p256dh.value = subscription.keys.p256dh;
		form.elements.auth.value = subscription.keys.auth;
		form.submit();
	};
	for (const form of settings.querySelectorAll(".push-form")) {
		form.addEventListener("submit", subscribe);
	}
}
// @license-end
