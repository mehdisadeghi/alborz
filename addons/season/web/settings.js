// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat
// iOS lets a page feel the phone's tilt only once the reader allows it from
// a tap, so ticking the season is where it asks (ADR 77). A refusal leaves
// the tilt out; the season falls as before.
(() => {
const box = document.getElementById("addon-season");
if (!window.DeviceOrientationEvent || typeof DeviceOrientationEvent.requestPermission !== "function") {
	return;
}
box.addEventListener("change", () => {
	if (box.checked) {
		DeviceOrientationEvent.requestPermission();
	}
});
})();
// @license-end
