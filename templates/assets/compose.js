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
	saveButton = document.getElementById("save-button");

const sendProgress = document.getElementById("send-progress");
composeForm.addEventListener("submit", ev => {
	[...document.querySelectorAll("input, textarea")].map(
		i => i.setAttribute("readonly", "readonly"));
	sendProgress.style.display = 'flex';
});

saveButton.addEventListener("click", ev => {
	sendProgress.querySelector(".info").innerText = "Saving draft...";
});

let attachments = [];

const headers = document.querySelector(".create-update .headers");

const attachmentsNode = document.getElementById("attachment-list");
const helpNode = attachmentsNode.querySelector(".help");

const attachmentsInput = headers.querySelector("input[type='file']");
attachmentsInput.removeAttribute("name");
attachmentsInput.addEventListener("input", ev => {
	const files = attachmentsInput.files;
	for (let i = 0; i < files.length; i++) {
		attachFile(files[i]);
	}
});

// The page takes the drop, not the window: the window outlives a boosted page.
composeForm.closest(".page-wrap").addEventListener("drop", ev => {
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

	if (complete) {
		sendButton.removeAttribute("disabled");
		saveButton.removeAttribute("disabled");
	} else {
		sendButton.setAttribute("disabled", "disabled");
		saveButton.setAttribute("disabled", "disabled");
	}

	attachmentUUIDsNode.value = attachments.
		filter(a => a.progress === 1.0).
		map(a => a.uuid).
		join(",");
}

function attachFile(file) {
	helpNode.remove();

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

	const handleError = msg => {
		attachments = attachments.filter(a => a !== attachment);
		node.classList.add("error");
		node.querySelector(".progress").remove();
		node.querySelector(".size").remove();
		node.querySelector("button").remove();
		node.querySelector(".error").innerText = "Error: " + msg;
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
			resp = { "error": "Error: invalid response" };
		}

		if (xhr.status !== 200) {
			handleError(resp["error"]);
			return;
		}

		attachment.uuid = resp[0].id;
		attachment.remove = resp[0].remove;
		updateState();
	});
	xhr.addEventListener("error", () => {
		handleError("an unexpected problem occured");
	});
	xhr.send(formData);

	updateState();
}

function removable(attachment) {
	attachment.node.querySelector("button").addEventListener("click", ev => {
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
	helpNode.remove();
	const attachment = { node: node, progress: 1.0, uuid: node.dataset.uuid, remove: node.dataset.remove };
	attachments.push(attachment);
	removable(attachment);
}
updateState();

function attachmentNodeFor(file) {
	const node = document.createElement("div"),
		progress = document.createElement("span"),
		filename = document.createElement("span"),
		error = document.createElement("span"),
		size = document.createElement("span"),
		button = document.createElement("button");
	node.classList.add("upload");

	progress.classList.add("progress");
	node.appendChild(progress);

	filename.classList.add("filename");
	filename.innerText = file.name;
	node.appendChild(filename);

	error.classList.add("error");
	node.appendChild(error);

	size.classList.add("size");
	size.innerText = formatSI(file.size) + "B";
	node.appendChild(size);

	button.innerHTML = "&times";
	node.appendChild(button);
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
