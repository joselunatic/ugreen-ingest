(() => {
  "use strict";

  const STATUS_LABEL = {
    DETECTED: "TARJETA DETECTADA",
    WAITING_CAMERA: "TARJETA DETECTADA",
    COPYING: "COPIANDO",
    VERIFYING: "VERIFICANDO",
    VERIFIED: "VERIFICADO",
    SAFE_TO_EJECT: "SEGURO RETIRAR",
    ERROR: "ERROR",
    EJECTED: "EXPULSADA",
  };

  const storageWidget = document.getElementById("storage-widget");
  const eventsBody = document.getElementById("events-body");
  const logBody = document.getElementById("log-body");
  const logFilter = document.getElementById("log-filter-event");
  const form = document.getElementById("new-event-form");
  const nameInput = document.getElementById("event-name");
  const camerasInput = document.getElementById("event-cameras");
  const formError = document.getElementById("event-form-error");

  let allIngests = [];

  function formatBytes(n) {
    if (!n || n <= 0) return "0 MB";
    const gb = n / (1024 ** 3);
    if (gb >= 1) return gb.toFixed(1) + " GB";
    const mb = n / (1024 ** 2);
    return mb.toFixed(0) + " MB";
  }

  function formatTime(iso) {
    if (!iso) return "";
    return new Date(iso).toLocaleString();
  }

  async function loadStorage() {
    try {
      const res = await fetch("/api/v1/status");
      const status = await res.json();
      const s = status.storage || {};
      const percent = s.percent_used || 0;
      const warn = percent >= 90;
      storageWidget.innerHTML = `
        <span class="mono">${s.path || ""}</span>
        <div class="storage-bar"><div class="storage-bar-fill${warn ? " warn" : ""}" style="width:${Math.min(100, percent).toFixed(0)}%"></div></div>
        <span>${formatBytes(s.bytes_free)} libres de ${formatBytes(s.bytes_total)} (${percent.toFixed(0)}% usado)</span>
      `;
    } catch (e) {
      storageWidget.textContent = "No se pudo consultar el almacenamiento: " + e.message;
    }
  }

  async function loadEvents() {
    const res = await fetch("/api/v1/projects");
    const events = await res.json();
    renderEvents(events || []);
    renderEventFilter(events || []);
  }

  function renderEvents(events) {
    eventsBody.innerHTML = "";
    for (const ev of events) {
      const tr = document.createElement("tr");
      const activateCell = ev.is_active
        ? `<span class="badge-active">ACTIVO</span>`
        : `<button class="btn btn-outline" data-activate="${ev.id}">Activar</button>`;
      tr.innerHTML = `
        <td>${escapeHtml(ev.name)}</td>
        <td>${(ev.cameras || []).join(", ")}</td>
        <td>${ev.card_count || 0}</td>
        <td>${formatBytes(ev.bytes_total)}</td>
        <td>${formatTime(ev.created_at)}</td>
        <td>${activateCell}</td>
      `;
      eventsBody.appendChild(tr);
    }
    eventsBody.querySelectorAll("[data-activate]").forEach((btn) => {
      btn.addEventListener("click", () => activateEvent(btn.dataset.activate, btn));
    });
  }

  function renderEventFilter(events) {
    const current = logFilter.value;
    logFilter.innerHTML = '<option value="">Todos</option>';
    for (const ev of events) {
      const opt = document.createElement("option");
      opt.value = ev.name;
      opt.textContent = ev.name;
      logFilter.appendChild(opt);
    }
    logFilter.value = current;
  }

  async function loadIngests() {
    const res = await fetch("/api/v1/ingests");
    allIngests = (await res.json()) || [];
    renderLog();
  }

  function renderLog() {
    const filter = logFilter.value;
    const rows = allIngests
      .filter((i) => !filter || i.project_name === filter)
      .sort((a, b) => b.id - a.id);

    logBody.innerHTML = "";
    for (const ing of rows) {
      const tr = document.createElement("tr");
      tr.innerHTML = `
        <td>${escapeHtml(ing.project_name || "-")}</td>
        <td>${escapeHtml(ing.camera_id || "-")}</td>
        <td>${escapeHtml(ing.card_number || "-")}</td>
        <td>${STATUS_LABEL[ing.status] || ing.status}</td>
        <td>${ing.verify_status || "-"}</td>
        <td>${ing.file_count || 0}</td>
        <td>${formatBytes(ing.bytes_total)}</td>
        <td class="mono">${escapeHtml(ing.destination_path || "-")}</td>
        <td>${formatTime(ing.detected_at)}</td>
        <td>${escapeHtml(ing.error_message || "")}</td>
      `;
      logBody.appendChild(tr);
    }
  }

  async function activateEvent(id, btn) {
    btn.disabled = true;
    try {
      const res = await fetch(`/api/v1/projects/${id}/activate`, { method: "POST" });
      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        alert("No se pudo activar el evento: " + (err.error || res.statusText));
        btn.disabled = false;
        return;
      }
      await loadEvents();
    } catch (e) {
      alert("Error de red: " + e.message);
      btn.disabled = false;
    }
  }

  function escapeHtml(s) {
    const div = document.createElement("div");
    div.textContent = String(s);
    return div.innerHTML;
  }

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    formError.textContent = "";
    const name = nameInput.value.trim();
    const cameras = camerasInput.value.split(",").map((c) => c.trim()).filter(Boolean);

    const submitBtn = form.querySelector("button[type=submit]");
    submitBtn.disabled = true;
    try {
      const res = await fetch("/api/v1/projects", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, cameras }),
      });
      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        formError.textContent = err.error || res.statusText;
        return;
      }
      form.reset();
      await loadEvents();
    } catch (e) {
      formError.textContent = "Error de red: " + e.message;
    } finally {
      submitBtn.disabled = false;
    }
  });

  logFilter.addEventListener("change", renderLog);

  async function init() {
    await Promise.all([loadStorage(), loadEvents(), loadIngests()]);
    setInterval(loadStorage, 5000);
    setInterval(loadEvents, 5000);
    setInterval(loadIngests, 5000);
  }

  init();
})();
