// @license magnet:?xt=urn:btih:d3d9a9a6595521f9666a5e94cc830dab83b65699&dn=expat.txt Expat

// A block of its own: a boosted visit reruns this script, and a top-level const declared twice is a SyntaxError.
{
const textarea = document.querySelector("textarea.body");
const composeForm = document.getElementById("compose-form");
if ("reply" in composeForm.dataset) {
	textarea.focus();
	textarea.setSelectionRange(textarea.value.length, textarea.value.length);
	textarea.scrollTop = textarea.scrollHeight;
}

const sendButton = document.getElementById("send-button"),
	saveButton = document.getElementById("save-button"),
	defaultButton = document.getElementById("send-default");
const signatureSelect = document.getElementById("signature");
const signatureApply = composeForm.querySelector('button[name="signature_apply"]');
signatureSelect.addEventListener("change", () => composeForm.requestSubmit(signatureApply));

const sendProgress = document.getElementById("send-progress");
composeForm.addEventListener("submit", ev => {
	if (ev.submitter === signatureApply) return;
	[...document.querySelectorAll("input, textarea")].map(
		i => i.setAttribute("readonly", "readonly"));
	sendProgress.style.display = 'flex';
});

saveButton.addEventListener("click", () => {
	const info = sendProgress.querySelector(".info");
	info.textContent = info.dataset.draft;
});

let attachments = [];

const headers = document.querySelector(".create-update .headers");

const attachmentsNode = document.getElementById("attachment-list");
const uploadRow = document.getElementById("upload-row");

const attachmentsInput = headers.querySelector("input[type='file']");
attachmentsInput.removeAttribute("name");
attachmentsInput.addEventListener("input", () => {
	const files = attachmentsInput.files;
	for (let i = 0; i < files.length; i++) {
		attachFile(files[i]);
	}
});

// The page takes the drop, not the window: the window outlives a boosted page.
const pageWrap = composeForm.closest(".page-wrap");
const writeArea = document.getElementById("write");
// The writing area names the drop while files are dragged over the page.
pageWrap.addEventListener("dragover", ev => writeArea.toggleAttribute("data-dragging", ev.dataTransfer.types.includes("Files")));
pageWrap.addEventListener("dragleave", ev => {
	if (!pageWrap.contains(ev.relatedTarget)) {
		writeArea.removeAttribute("data-dragging");
	}
});
pageWrap.addEventListener("drop", ev => {
	writeArea.removeAttribute("data-dragging");
	// A drop on the mail rail imports; one on the file field is the field's own.
	if (ev.target.closest("aside[data-mail-sidebar], input[type='file']")) {
		return;
	}
	const files = ev.dataTransfer.files;
	for (let i = 0; i < files.length; i++) {
		attachFile(files[i]);
	}
});

const attachmentUUIDsNode = document.getElementById("attachment-uuids");
function updateState() {
	let complete = true;
	for (let i = 0; i < attachments.length; i++) {
		const a = attachments[i];
		const progress = a.node.querySelector(".progress");
		progress.style.width = `${Math.floor(a.progress * 100)}%`;
		complete &= a.progress === 1.0;
		if (a.progress === 1.0) {
			progress.style.display = 'none';
		}
	}

	for (const control of [sendButton, saveButton, defaultButton, signatureSelect]) {
		control.toggleAttribute("disabled", !complete);
	}

	attachmentUUIDsNode.value = attachments.
		filter(a => a.progress === 1.0).
		map(a => a.uuid).
		join(",");
}

function attachFile(file) {
	const xhr = new XMLHttpRequest();
	const node = attachmentNodeFor(file);
	const attachment = {
		node: node,
		progress: 0,
		xhr: xhr,
	};
	attachments.push(attachment);
	attachmentsNode.appendChild(node);
	removable(attachment);

	let formData = new FormData();
	formData.append("attachments", file);

	// The server's reason, when it gave one, is a detail under the translated statement.
	const handleError = msg => {
		attachments = attachments.filter(a => a !== attachment);
		node.classList.add("error");
		node.querySelector(".progress").remove();
		node.querySelector(".size").remove();
		node.querySelector("button").remove();
		const error = node.querySelector(".error");
		error.textContent = uploadRow.dataset.failed;
		if (msg) {
			error.title = msg;
		}
		updateState();
	};

	xhr.open("POST", composeForm.dataset.upload);
	xhr.upload.addEventListener("progress", ev => {
		attachment.progress = ev.loaded / ev.total;
		updateState();
	});
	xhr.addEventListener("load", () => {
		let resp;
		try {
			resp = JSON.parse(xhr.responseText);
		} catch {
			resp = {};
		}

		if (xhr.status !== 200) {
			handleError(resp.error);
			return;
		}

		attachment.uuid = resp[0].id;
		attachment.remove = resp[0].remove;
		updateState();
	});
	xhr.addEventListener("error", () => handleError());
	xhr.send(formData);

	updateState();
}

function removable(attachment) {
	attachment.node.querySelector("button").addEventListener("click", () => {
		attachment.xhr?.abort();
		attachments = attachments.filter(a => a !== attachment);
		attachment.node.remove();
		updateState();

		if (typeof attachment.uuid !== "undefined") {
			const cancel = new XMLHttpRequest();
			cancel.open("POST", attachment.remove);
			cancel.send();
		}
	});
}

// A refused send renders what it held; those uploads are finished.
for (const node of attachmentsNode.querySelectorAll(".upload[data-uuid]")) {
	const attachment = { node: node, progress: 1.0, uuid: node.dataset.uuid, remove: node.dataset.remove };
	attachments.push(attachment);
	removable(attachment);
}
updateState();

// The server renders the row, its labels translated; the script fills in the file.
function attachmentNodeFor(file) {
	const node = uploadRow.content.firstElementChild.cloneNode(true);
	node.querySelector(".filename").textContent = file.name;
	node.querySelector(".size").textContent = formatSI(file.size) + "B";
	return node;
}

// via https://github.com/ThomWright/format-si-prefix; MIT license
// Copyright (c) 2015 Thom Wright
const PREFIXES = {
  '24': 'Y', '21': 'Z', '18': 'E', '15': 'P', '12': 'T', '9': 'G', '6': 'M',
  '3': 'k', '0': '', '-3': 'm', '-6': 'µ', '-9': 'n', '-12': 'p', '-15': 'f',
  '-18': 'a', '-21': 'z', '-24': 'y'
};

function formatSI(num) {
  if (num === 0) {
    return '0';
  }
  let sig = Math.abs(num);
  let exponent = 0;
  while (sig >= 1000 && exponent < 24) {
    sig /= 1000;
    exponent += 3;
  }
  while (sig < 1 && exponent > -24) {
    sig *= 1000;
    exponent -= 3;
  }
  const signPrefix = num < 0 ? '-' : '';
  if (sig > 1000) {
    return signPrefix + sig.toFixed(0) + PREFIXES[exponent];
  }
  return signPrefix + parseFloat(sig.toPrecision(3)) + PREFIXES[exponent];
}
}

// @license-end
