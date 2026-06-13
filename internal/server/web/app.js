const $ = (id) => document.getElementById(id);
const toast = (msg) => {
  $("toast").textContent = msg;
  $("toast").classList.remove("hidden");
  setTimeout(() => $("toast").classList.add("hidden"), 3600);
};

const fields = [
  ["Name", "text"], ["DeviceID", "text"], ["Port", "text"], ["IPAddress", "text"],
  ["LineupInterval", "duration"], ["GuideInterval", "duration"], ["GuideDays", "number"],
  ["CreateXML", "checkbox"], ["IncludePseudoTVGuide", "checkbox"], ["IncludeOTT", "checkbox"],
  ["LogLevel", "select"], ["SaveLog", "checkbox"], ["OutDir", "text"], ["TabloDevice", "text"]
];

let currentConfig = {};

async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...opts
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

function showApp(show) {
  $("loginPanel").classList.toggle("hidden", show);
  $("appPanel").classList.toggle("hidden", !show);
  $("logout").classList.toggle("hidden", !show);
}

async function loadSession() {
  const session = await api("/admin/api/session");
  showApp(session.authenticated);
  if (session.authenticated) {
    await Promise.all([loadConfig(), loadStatus()]);
  }
}

async function loadStatus() {
  const status = await api("/admin/api/status");
  $("serverURL").textContent = status.serverURL || "-";
  $("tunerCount").textContent = status.tunerCount ?? "-";
  $("proxyReady").textContent = status.proxyReady ? "Yes" : "No";
  $("activeStreams").textContent = status.activeStreams ?? "-";
  $("restartPending").textContent = status.restartPending ? "Yes" : "No";
  $("summary").textContent = !status.proxyReady ? "Admin ready. Complete Tablo setup to start the proxy." : status.restartPending ? "Proxy running. Restart required for some changes." : "Proxy running.";
}

async function loadConfig() {
  const data = await api("/admin/api/config");
  currentConfig = data.config;
  renderConfig(currentConfig);
}

function renderConfig(cfg) {
  const form = $("configForm");
  form.innerHTML = "";
  for (const [name, kind] of fields) {
    const label = document.createElement("label");
    label.textContent = name;
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
      input.type = kind === "checkbox" ? "checkbox" : kind === "number" ? "number" : "text";
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
    const input = document.querySelector(`[name="${name}"]`);
    if (kind === "checkbox") next[name] = input.checked;
    else if (kind === "number") next[name] = Number(input.value || 0);
    else next[name] = input.value;
  }
  return next;
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
    toast("Logged in");
    await loadSession();
  } catch (err) {
    toast(err.message.trim());
  }
});

$("logout").addEventListener("click", async () => {
  await api("/admin/api/logout", { method: "POST", body: "{}" });
  showApp(false);
});

$("configForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    const data = await api("/admin/api/config", { method: "PUT", body: JSON.stringify(readConfigForm()) });
    currentConfig = data.config;
    renderConfig(currentConfig);
    await loadStatus();
    toast(data.restartPending ? "Saved. Restart required for some fields." : "Settings saved");
  } catch (err) {
    toast(err.message.trim());
  }
});

$("tabloLoginForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    const data = await api("/admin/api/tablo/login", { method: "POST", body: JSON.stringify(Object.fromEntries(new FormData(event.target))) });
    const list = $("deviceList");
    list.innerHTML = "";
    for (const device of data.devices || []) {
      const row = document.createElement("div");
      row.className = "device";
      row.innerHTML = `<span><strong>${device.name || "Tablo"}</strong><br>${device.serverId || ""} ${device.url || ""}</span>`;
      const button = document.createElement("button");
      button.textContent = "Select";
      button.onclick = async () => {
        await api("/admin/api/tablo/select-device", { method: "POST", body: JSON.stringify({ serverId: device.serverId }) });
        toast("Device selected");
        await Promise.all([loadConfig(), loadStatus()]);
      };
      row.appendChild(button);
      list.appendChild(row);
    }
  } catch (err) {
    toast(err.message.trim());
  }
});

$("refreshLineup").onclick = async () => {
  await api("/admin/api/actions/refresh-lineup", { method: "POST", body: "{}" });
  toast("Lineup refresh complete");
  loadStatus();
};
$("refreshGuide").onclick = async () => {
  await api("/admin/api/actions/refresh-guide", { method: "POST", body: "{}" });
  toast("Guide refresh complete");
  loadStatus();
};
$("reloadStatus").onclick = loadStatus;
$("reloadLogs").onclick = async () => {
  const data = await api("/admin/api/logs");
  $("logOutput").textContent = (data.lines || []).join("\n");
};

loadSession().catch((err) => toast(err.message.trim()));
