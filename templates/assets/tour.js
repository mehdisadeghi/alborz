// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// The tour knows no page: a stop points at the first visible control marked with its data-tour (ADR 76).
(() => {
	const still = matchMedia("(prefers-reduced-motion: reduce)");
	const anchored = CSS.supports("position-area", "block-end");
	const param = "tour";
	let lit = null;

	const typing = el => el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName);
	const dialog = () => document.getElementById("tour");
	const shown = tour => tour.querySelector(".tour-step:not([hidden])");
	const visible = el => el.checkVisibility({ visibilityProperty: true }) && el.getClientRects().length > 0;
	const targetOf = step => {
		const mark = step.dataset.target;
		return mark ? [...document.querySelectorAll(`[data-tour~="${CSS.escape(mark)}"]`)].find(visible) : null;
	};

	const unlight = () => {
		if (lit) {
			lit.classList.remove("tour-target");
			lit.style.removeProperty("anchor-name");
			lit = null;
		}
	};

	// Without anchor positioning the stop is placed under its target by hand, or above it where the viewport ends.
	const place = (tour, target) => {
		tour.style.removeProperty("top");
		tour.style.removeProperty("left");
		if (anchored || !target || matchMedia("(width <= 600px)").matches) {
			return;
		}
		const box = target.getBoundingClientRect();
		const gap = 8;
		const below = box.bottom + gap + tour.offsetHeight <= innerHeight;
		tour.style.top = `${below ? box.bottom + gap : Math.max(gap, box.top - gap - tour.offsetHeight)}px`;
		tour.style.left = `${Math.min(Math.max(gap, box.left), innerWidth - tour.offsetWidth - gap)}px`;
	};

	const address = name => {
		const url = new URL(location.href);
		if (name) {
			url.searchParams.set(param, name);
		} else {
			url.searchParams.delete(param);
		}
		history.replaceState(history.state, "", url);
	};

	const show = (tour, step, focus) => {
		for (const s of tour.querySelectorAll(".tour-step")) {
			s.hidden = s !== step;
		}
		unlight();
		const target = targetOf(step);
		tour.classList.toggle("tour-anchored", Boolean(target));
		if (target) {
			lit = target;
			target.classList.add("tour-target");
			target.style.setProperty("anchor-name", "--tour");
			target.scrollIntoView({ block: "nearest", behavior: still.matches ? "instant" : "smooth" });
		}
		place(tour, target);
		if (focus) {
			step.querySelector(".tour-next").focus({ preventScroll: true });
		}
	};

	// A neighbour on this page is shown in place; one on another page is a boosted navigation.
	const samePage = link => {
		const to = new URL(link.href);
		const here = new URL(location.href);
		to.searchParams.delete(param);
		here.searchParams.delete(param);
		return to.pathname === here.pathname && to.search === here.search;
	};

	const go = (tour, link) => {
		if (!samePage(link)) {
			link.click();
			return;
		}
		show(tour, tour.querySelector(`.tour-step[data-stop="${CSS.escape(link.dataset.stop)}"]`), true);
		address(link.dataset.stop);
	};

	document.addEventListener("click", ev => {
		const link = ev.target.closest("#tour a[data-stop]");
		if (link && samePage(link) && !ev.ctrlKey && !ev.metaKey && !ev.shiftKey) {
			ev.preventDefault();
			ev.stopPropagation();
			go(dialog(), link);
		}
	}, true);

	// close does not bubble.
	document.addEventListener("close", ev => {
		if (ev.target.id === "tour") {
			unlight();
			address("");
			document.getElementById("main").focus({ preventScroll: true });
		}
	}, true);

	document.addEventListener("keydown", ev => {
		const tour = dialog();
		if (!tour || !tour.open || ev.ctrlKey || ev.metaKey || ev.altKey || ev.isComposing || typing(ev.target) ||
			document.querySelector("dialog:modal, [popover]:popover-open")) {
			return;
		}
		const step = shown(tour);
		const rtl = document.documentElement.dir === "rtl";
		const toward = { ArrowRight: rtl ? ".tour-back" : ".tour-next", ArrowLeft: rtl ? ".tour-next" : ".tour-back" }[ev.key];
		if (ev.key === "Escape") {
			ev.preventDefault();
			tour.close();
		} else if (toward || (ev.key === "Enter" && !ev.target.closest("a, button, input, textarea, select"))) {
			const control = step.querySelector(toward || ".tour-next");
			if (!control) {
				return;
			}
			ev.preventDefault();
			if (control.dataset.stop) {
				go(tour, control);
			} else {
				control.click();
			}
		}
	});

	// A boosted page brings the tour at its stop and takes the focus; a swap into main may replace what it points at.
	const start = elt => {
		const tour = dialog();
		if (!tour || !tour.open) {
			unlight();
		} else if (elt.contains(tour)) {
			show(tour, shown(tour), true);
		} else if (!lit || !lit.isConnected) {
			show(tour, shown(tour), false);
		}
	};
	document.addEventListener("htmx:load", ev => start(ev.detail.elt));
	addEventListener("resize", () => {
		const tour = dialog();
		if (tour && tour.open) {
			place(tour, lit);
		}
	});
	start(document.body);
})();
// @license-end
