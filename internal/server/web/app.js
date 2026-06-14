const $ = (id) => document.getElementById(id);

const fields = [
  ["Name", "text", "Device name"],
  ["DeviceID", "text", "HDHomeRun device ID"],
  ["Port", "text", "HTTP port"],
  ["IPAddress", "text", "Advertised IP address"],
  ["LineupIntervalDays", "number", "Lineup refresh days"],
  ["GuideIntervalHours", "number", "Guide refresh hours"],
  ["GuideDays", "number", "Guide days"],
  ["CreateXML", "checkbox", "Create XMLTV guide"],
  ["IncludePseudoTVGuide", "checkbox", "Include PseudoTV guide"],
  ["IncludeOTT", "checkbox", "Include OTT channels"],
  ["LogLevel", "select", "Log level"],
  ["OutDir", "text", "Output directory"],
  ["TabloDevice", "text", "Selected Tablo device"]
];

let currentConfig = {};

function toast(message) {
  $("toast").textContent = message;
  $("toast").classList.remove("hidden");
  setTimeout(() => $("toast").classList.add("hidden"), 3600);
}

function showInline(id, message, tone = "info") {
  const el = $(id);
  el.textContent = message;
  el.className = `inline-message ${tone}`;
  el.classList.remove("hidden");
}

function hideInline(id) {
  $(id).classList.add("hidden");
}

async function api(path, opts = {}) {
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    ...opts
  });
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return res.json();
}

function showView(view) {
  $("loginPanel").classList.toggle("hidden", view !== "login");
  $("setupPanel").classList.toggle("hidden", view !== "setup");
  $("appPanel").classList.toggle("hidden", view !== "app");
  $("logout").classList.toggle("hidden", view === "login");
  if (view === "setup") {
    $("tabloLoginForm").reset();
    $("deviceList").innerHTML = "";
    hideInline("setupMessage");
  }
}

async function loadSession() {
  const session = await api("/admin/api/session");
  if (!session.authenticated) {
    showView("login");
    $("summary").textContent = session.passwordConfigured ? "Sign in to manage your proxy." : "Create the admin password to begin.";
    return;
  }
  if (!session.tabloConfigured) {
    showView("setup");
    $("summary").textContent = "Connect Tablo to unlock the proxy dashboard.";
    return;
  }
  showView("app");
  await Promise.all([loadConfig(), loadStatus()]);
}

async function loadStatus() {
  const status = await api("/admin/api/status");
  if (!status.tabloConfigured) {
    showView("setup");
    $("summary").textContent = "Connect Tablo to unlock the proxy dashboard.";
    return;
  }
  $("serverURL").textContent = status.serverURL || "-";
  $("tunerCount").textContent = status.tunerCount ?? "-";
  $("proxyReady").textContent = status.proxyReady ? "Online" : "Setup needed";
  $("activeStreams").textContent = status.activeStreams ?? "-";
  $("restartPending").textContent = status.restartPending ? "Yes" : "No";
  $("summary").textContent = status.proxyReady
    ? status.restartPending
      ? "Proxy running. Restart required for some changes."
      : "Proxy running and ready for Plex."
    : "Tablo is configured. Proxy activation needs attention.";
}

async function loadConfig() {
  const data = await api("/admin/api/config");
  currentConfig = data.config;
  renderConfig(currentConfig);
}

function renderConfig(cfg) {
  const form = $("configForm");
  form.innerHTML = "";
  for (const [name, kind, labelText] of fields) {
    const label = document.createElement("label");
    label.textContent = labelText;
    let input;
    if (kind === "select") {
      input = document.createElement("select");
      for (const value of ["info", "error", "warn", "debug"]) {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = value;
        input.appendChild(option);
      }
    } else {
      input = document.createElement("input");
      input.type = kind === "checkbox" ? "checkbox" : kind;
    }
    input.name = name;
    if (kind === "checkbox") input.checked = Boolean(cfg[name]);
    else input.value = cfg[name] ?? "";
    label.appendChild(input);
    form.appendChild(label);
  }
}

function readConfigForm() {
  const next = { ...currentConfig };
  for (const [name, kind] of fields) {
    const input = document.querySelector(`#configForm [name="${name}"]`);
    if (kind === "checkbox") next[name] = input.checked;
    else if (kind === "number") next[name] = Number(input.value || 0);
    else next[name] = input.value;
  }
  return next;
}

function renderDevices(containerID, messageID, devices) {
  const list = $(containerID);
  list.innerHTML = "";
  if (!devices.length) {
    showInline(messageID, "No Tablo devices were found for this account.", "warn");
    return;
  }
  $("deviceStep").classList.add("active");
  showInline(messageID, "Select the Tablo device this proxy should use.", "success");
  for (const device of devices) {
    const row = document.createElement("article");
    row.className = "device";
    row.innerHTML = `
      <div>
        <strong>${escapeHTML(device.name || "Tablo")}</strong>
        <span>${escapeHTML(device.serverId || "Unknown server")} · ${escapeHTML(device.url || "No URL")}</span>
      </div>
    `;
    const button = document.createElement("button");
    button.textContent = "Use this device";
    button.onclick = async () => {
      button.disabled = true;
      button.textContent = "Connecting...";
      try {
        await api("/admin/api/tablo/select-device", { method: "POST", body: JSON.stringify({ serverId: device.serverId }) });
        toast("Device selected. Dashboard unlocked.");
        await loadSession();
      } catch (err) {
        button.disabled = false;
        button.textContent = "Use this device";
        showInline(messageID, err.message, "error");
      }
    };
    row.appendChild(button);
    list.appendChild(row);
  }
}

function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, (char) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#039;"
  })[char]);
}

async function submitTabloLogin(form, containerID, messageID) {
  hideInline(messageID);
  $(containerID).innerHTML = "";
  const button = form.querySelector("button[type='submit']");
  const previous = button.textContent;
  button.disabled = true;
  button.textContent = "Scanning...";
  try {
    const data = await api("/admin/api/tablo/login", { method: "POST", body: JSON.stringify(Object.fromEntries(new FormData(form))) });
    renderDevices(containerID, messageID, data.devices || []);
  } catch (err) {
    showInline(messageID, err.message, "error");
  } finally {
    button.disabled = false;
    button.textContent = previous;
  }
}

document.querySelectorAll(".tabs button").forEach((button) => {
  button.addEventListener("click", () => {
    document.querySelectorAll(".tabs button").forEach((b) => b.classList.remove("active"));
    document.querySelectorAll(".tab").forEach((tab) => tab.classList.remove("active"));
    button.classList.add("active");
    $(button.dataset.tab).classList.add("active");
  });
});

$("loginForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    await api("/admin/api/login", { method: "POST", body: JSON.stringify(Object.fromEntries(new FormData(event.target))) });
    await loadSession();
  } catch (err) {
    toast(err.message);
  }
});

$("logout").addEventListener("click", async () => {
  await api("/admin/api/logout", { method: "POST", body: "{}" });
  showView("login");
  $("summary").textContent = "Signed out.";
});

$("configForm").addEventListener("submit", async (event) => {
	event.preventDefault();
	try {
		const data = await api("/admin/api/config", { method: "PUT", body: JSON.stringify(readConfigForm()) });
    currentConfig = data.config;
    renderConfig(currentConfig);
    await loadStatus();
    toast(data.restartPending ? "Saved. Restart required for some fields." : "Settings saved.");
  } catch (err) {
    toast(err.message);
	}
});

$("passwordForm").addEventListener("submit", async (event) => {
	event.preventDefault();
	const form = event.target;
	const values = Object.fromEntries(new FormData(form));
	if (values.newPassword !== values.confirmPassword) {
		toast("New passwords do not match.");
		return;
	}
	try {
		await api("/admin/api/password", {
			method: "POST",
			body: JSON.stringify({
				currentPassword: values.currentPassword,
				newPassword: values.newPassword
			})
		});
		form.reset();
		toast("Password changed. Sign in again.");
		showView("login");
		$("summary").textContent = "Password changed. Sign in with the new password.";
	} catch (err) {
		toast(err.message);
	}
});

$("tabloLoginForm").addEventListener("submit", (event) => {
	event.preventDefault();
	submitTabloLogin(event.target, "deviceList", "setupMessage");
});

$("dashboardTabloLoginForm").addEventListener("submit", (event) => {
  event.preventDefault();
  submitTabloLogin(event.target, "dashboardDeviceList", "dashboardSetupMessage");
});

$("refreshLineup").onclick = async () => {
  try {
    await api("/admin/api/actions/refresh-lineup", { method: "POST", body: "{}" });
    toast("Lineup refresh complete.");
    loadStatus();
  } catch (err) {
    toast(err.message);
  }
};

$("refreshGuide").onclick = async () => {
  try {
    await api("/admin/api/actions/refresh-guide", { method: "POST", body: "{}" });
    toast("Guide refresh complete.");
    loadStatus();
  } catch (err) {
    toast(err.message);
  }
};

$("reloadStatus").onclick = loadStatus;
loadSession().catch((err) => toast(err.message));
