(() => {
  "use strict";

  const ACTIVE_STATUSES = new Set([
    "DETECTED", "WAITING_CAMERA", "COPYING", "VERIFYING", "VERIFIED", "SAFE_TO_EJECT", "ERROR", "CANCELLED",
  ]);

  const STATUS_LABEL = {
    DETECTED: "TARJETA DETECTADA",
    WAITING_CAMERA: "TARJETA DETECTADA",
    COPYING: "COPIANDO",
    VERIFYING: "VERIFICANDO",
    VERIFIED: "VERIFICADO",
    SAFE_TO_EJECT: "SEGURO RETIRAR",
    ERROR: "ERROR",
    CANCELLED: "CANCELADA",
    EJECTED: "EXPULSADA",
  };

  const STATUS_CLASS = {
    DETECTED: "waiting",
    WAITING_CAMERA: "waiting",
    COPYING: "copying",
    VERIFYING: "verifying",
    VERIFIED: "verified",
    SAFE_TO_EJECT: "safe",
    ERROR: "error",
    CANCELLED: "cancelled",
    EJECTED: "ejected",
  };

  // Small stroke-based icons, inlined per status so they need no external
  // sprite/shadow-DOM styling tricks. currentColor picks up the pill's
  // text color automatically.
  const ICON_CAMERA = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 8h3l1.5-2h7L17 8h3a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1Z"/><circle cx="12" cy="13" r="3.2"/></svg>';
  const ICON_SPINNER = '<svg class="spin" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><path d="M12 3a9 9 0 1 1-6.36 2.64"/></svg>';
  const ICON_CHECK = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><path class="draw" d="M4 12.5l5 5L20 6"/></svg>';
  const ICON_WARNING = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3.5 2 20.5h20L12 3.5Z"/><path d="M12 10v4.5"/><circle cx="12" cy="17.5" r="0.6" fill="currentColor" stroke="none"/></svg>';
  const ICON_PAUSE = '<svg viewBox="0 0 24 24" fill="currentColor" stroke="none"><rect x="6" y="5" width="4" height="14" rx="1"/><rect x="14" y="5" width="4" height="14" rx="1"/></svg>';

  const STATUS_ICON = {
    DETECTED: ICON_CAMERA,
    WAITING_CAMERA: ICON_CAMERA,
    COPYING: ICON_SPINNER,
    VERIFYING: ICON_SPINNER,
    VERIFIED: ICON_CHECK,
    SAFE_TO_EJECT: ICON_CHECK,
    ERROR: ICON_WARNING,
    CANCELLED: ICON_PAUSE,
  };

  const ingests = new Map();      // id -> ingest record (server truth)
  const liveProgress = new Map(); // id -> latest progress event (transient)
  const cardEls = new Map();      // id -> DOM element
  const lastStatus = new Map();   // id -> previous status, to gate one-shot effects
  let cameras = [];
  let assigning = new Set();

  const activeArea = document.getElementById("active-area");
  const idleState = document.getElementById("idle-state");
  const idleStats = document.getElementById("idle-stats");
  const historyBody = document.getElementById("history-body");
  const cardTemplate = document.getElementById("card-template");
  const projectNameEl = document.getElementById("project-name");
  const healthDot = document.getElementById("health-dot");
  const healthLabel = document.getElementById("health-label");
  const storageDot = document.getElementById("storage-dot");
  const storageLabel = document.getElementById("storage-label");

  const STORAGE_WARN_PERCENT = 90;

  function cameraLabel(code) {
    const m = /^CAM0*(\d+)$/.exec(code);
    if (m) return "CAM " + m[1];
    return code;
  }

  function formatBytes(n) {
    if (!n || n <= 0) return "0 MB";
    const gb = n / (1024 ** 3);
    if (gb >= 1) return gb.toFixed(1) + " GB";
    const mb = n / (1024 ** 2);
    return mb.toFixed(0) + " MB";
  }

  function formatSpeed(bytesPerSec) {
    if (!bytesPerSec || bytesPerSec <= 0) return "";
    return (bytesPerSec / (1024 ** 2)).toFixed(0) + " MB/s";
  }

  function formatTime(iso) {
    if (!iso) return "";
    const d = new Date(iso);
    return d.toLocaleString("es-ES");
  }

  function pluralize(count, singular, plural) {
    return count === 1 ? singular : plural;
  }

  async function loadConfig() {
    const res = await fetch("/api/v1/config");
    const cfg = await res.json();
    cameras = cfg.cameras || [];
    projectNameEl.textContent = "Proyecto: " + (cfg.project_name || "");
  }

  async function loadIngests() {
    const res = await fetch("/api/v1/ingests");
    const list = await res.json();
    ingests.clear();
    for (const ing of list || []) {
      ingests.set(ing.id, ing);
    }
    render();
  }

  function connectEvents() {
    const es = new EventSource("/api/v1/events");
    es.addEventListener("ingest_updated", (e) => {
      const data = JSON.parse(e.data);
      const ing = data.ingest;
      ingests.set(ing.id, ing);
      if (ing.status !== "COPYING") liveProgress.delete(ing.id);
      render();
    });
    es.addEventListener("progress", (e) => {
      const data = JSON.parse(e.data);
      liveProgress.set(data.progress.ingest_id, data.progress);
      render();
    });
    es.onerror = () => {
      // EventSource retries automatically; nothing to do here.
    };
  }

  function render() {
    const all = Array.from(ingests.values());
    const active = all.filter((i) => ACTIVE_STATUSES.has(i.status)).sort((a, b) => a.id - b.id);
    const history = all.slice().sort((a, b) => b.id - a.id);

    idleState.style.display = active.length === 0 ? "" : "none";
    if (active.length === 0) renderIdleStats(all);

    const seen = new Set();
    for (const ing of active) {
      seen.add(ing.id);
      renderCard(ing);
    }
    for (const [id, el] of cardEls) {
      if (!seen.has(id)) {
        el.remove();
        cardEls.delete(id);
        lastStatus.delete(id);
      }
    }

    renderHistory(history);
  }

  function renderIdleStats(all) {
    const todayKey = new Date().toDateString();
    let count = 0;
    let bytes = 0;
    for (const ing of all) {
      if (!ing.detected_at || new Date(ing.detected_at).toDateString() !== todayKey) continue;
      count++;
      if (ing.status === "SAFE_TO_EJECT" || ing.status === "EJECTED") {
        bytes += ing.bytes_copied || 0;
      }
    }
    idleStats.textContent = count > 0
      ? `Hoy: ${count} ${pluralize(count, "tarjeta", "tarjetas")} · ${formatBytes(bytes)} verificados`
      : "";
  }

  function renderCard(ing) {
    let el = cardEls.get(ing.id);
    if (!el) {
      el = cardTemplate.content.firstElementChild.cloneNode(true);
      activeArea.appendChild(el);
      cardEls.set(ing.id, el);
    }

    const statusChanged = lastStatus.get(ing.id) !== ing.status;
    lastStatus.set(ing.id, ing.status);

    const statusClass = STATUS_CLASS[ing.status] || "";
    el.querySelector(".status-pill").className = "status-pill " + statusClass;
    if (statusChanged) {
      el.querySelector(".pill-icon").innerHTML = STATUS_ICON[ing.status] || "";
      triggerTransitionEffect(el, ing.status);
    }
    el.querySelector(".status-pill-text").textContent = STATUS_LABEL[ing.status] || ing.status;

    const title = ing.camera_id
      ? `${cameraLabel(ing.camera_id)} - ${ing.card_number}`
      : `${ing.source_identifier} · ${formatBytes(ing.bytes_total)}`;
    el.querySelector(".card-title").textContent = title;

    el.classList.toggle("show-no-eject", ing.status === "COPYING" || ing.status === "VERIFYING");
    el.classList.toggle("show-camera-picker", ing.status === "WAITING_CAMERA" || ing.status === "DETECTED");
    el.classList.toggle("show-progress", ["COPYING", "VERIFYING", "VERIFIED", "SAFE_TO_EJECT"].includes(ing.status));
    el.classList.toggle("show-error", ing.status === "ERROR");
    el.classList.toggle("show-cancelled-msg", ing.status === "CANCELLED");
    el.classList.toggle("show-actions", ing.status === "SAFE_TO_EJECT");
    el.classList.toggle("show-cancel-action", ing.status === "COPYING" || ing.status === "VERIFYING");
    el.classList.toggle("show-retry-action", ing.status === "ERROR" || ing.status === "CANCELLED");
    el.classList.toggle("state-copying", ing.status === "COPYING");
    el.classList.toggle("state-verifying", ing.status === "VERIFYING");

    if (ing.status === "WAITING_CAMERA" || ing.status === "DETECTED") {
      renderCameraButtons(el, ing);
    }
    if (el.classList.contains("show-progress")) {
      renderProgress(el, ing);
    }
    if (ing.status === "ERROR") {
      el.querySelector(".error-block").textContent = ing.error_message || "Error desconocido.";
    }
    if (ing.status === "CANCELLED") {
      el.querySelector(".cancelled-block").textContent = ing.error_message || "Operación cancelada.";
    }

    const ejectBtn = el.querySelector(".btn-eject");
    ejectBtn.onclick = () => doEject(ing.id, ejectBtn);
    ejectBtn.disabled = false;

    const cancelBtn = el.querySelector(".btn-cancel");
    cancelBtn.onclick = () => doCancel(ing.id, cancelBtn, ing.status);
    cancelBtn.disabled = false;

    const retryBtn = el.querySelector(".btn-retry");
    retryBtn.onclick = () => doRetry(ing.id, retryBtn);
    retryBtn.disabled = false;
  }

  // Brief, one-shot feedback exactly when a card crosses into SAFE_TO_EJECT
  // or ERROR - never replayed on unrelated re-renders (gated by
  // statusChanged in renderCard), and never looping.
  function triggerTransitionEffect(el, status) {
    if (status === "SAFE_TO_EJECT") {
      restartAnimation(el, "pulse-success");
    } else if (status === "ERROR") {
      restartAnimation(el, "shake-error");
    }
  }

  function restartAnimation(el, className) {
    el.classList.remove(className);
    void el.offsetWidth; // force reflow so the same class can retrigger
    el.classList.add(className);
  }

  function renderCameraButtons(el, ing) {
    const wrap = el.querySelector(".camera-buttons");
    if (wrap.dataset.built === "1") return; // buttons are static per card, build once
    wrap.dataset.built = "1";
    wrap.innerHTML = "";
    for (const cam of cameras) {
      const btn = document.createElement("button");
      btn.className = "btn-camera";
      btn.textContent = cameraLabel(cam);
      btn.onclick = () => doAssignCamera(ing.id, cam, wrap);
      wrap.appendChild(btn);
    }
  }

  function renderProgress(el, ing) {
    const live = liveProgress.get(ing.id);
    const total = ing.bytes_total || 0;
    const copied = (live && live.bytes_copied) || ing.bytes_copied || 0;

    let percent;
    if (["VERIFYING", "VERIFIED", "SAFE_TO_EJECT"].includes(ing.status)) {
      percent = 100;
    } else {
      percent = live ? live.percent : (total > 0 ? (copied / total) * 100 : 0);
    }
    percent = Math.min(100, Math.max(0, percent));

    el.querySelector(".progress-fill").style.width = percent.toFixed(0) + "%";
    el.querySelector(".progress-percent").textContent = percent.toFixed(0) + "%";
    el.querySelector(".progress-bytes").textContent = `${formatBytes(copied)} / ${formatBytes(total)}`;

    const speedEl = el.querySelector(".progress-speed");
    const filesEl = el.querySelector(".progress-files");
    if (ing.status === "COPYING") {
      speedEl.textContent = live ? formatSpeed(live.speed_bytes_per_sec) : "";
      filesEl.textContent = `${(live && live.files_copied) || 0} / ${ing.file_count || 0} archivos`;
    } else if (ing.status === "VERIFYING") {
      speedEl.textContent = "Verificando…";
      filesEl.textContent = `${ing.file_count || 0} archivos`;
    } else {
      speedEl.textContent = "";
      filesEl.textContent = `${ing.file_count || 0} archivos`;
    }
  }

  function renderHistory(history) {
    historyBody.innerHTML = "";
    for (const ing of history) {
      const tr = document.createElement("tr");
      tr.innerHTML = `
        <td>${ing.camera_id ? cameraLabel(ing.camera_id) : "-"}</td>
        <td>${ing.card_number || "-"}</td>
        <td>${STATUS_LABEL[ing.status] || ing.status}</td>
        <td>${ing.verify_status || "-"}</td>
        <td>${ing.file_count || 0}</td>
        <td>${formatBytes(ing.bytes_total)}</td>
        <td>${formatTime(ing.detected_at)}</td>
      `;
      historyBody.appendChild(tr);
    }
  }

  async function doAssignCamera(id, camera, wrap) {
    if (assigning.has(id)) return;
    assigning.add(id);
    wrap.querySelectorAll("button").forEach((b) => (b.disabled = true));
    try {
      const res = await fetch(`/api/v1/ingests/${id}/assign-camera`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ camera }),
      });
      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        alert("No se ha podido asignar la cámara: " + (err.error || res.statusText));
        wrap.querySelectorAll("button").forEach((b) => (b.disabled = false));
      }
    } catch (e) {
      alert("Error de red al asignar la cámara: " + e.message);
      wrap.querySelectorAll("button").forEach((b) => (b.disabled = false));
    } finally {
      assigning.delete(id);
    }
  }

  async function doEject(id, btn) {
    btn.disabled = true;
    try {
      const res = await fetch(`/api/v1/ingests/${id}/eject`, { method: "POST" });
      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        alert("No se ha podido expulsar: " + (err.error || res.statusText));
        btn.disabled = false;
      }
    } catch (e) {
      alert("Error de red al expulsar: " + e.message);
      btn.disabled = false;
    }
  }

  async function doCancel(id, btn, status) {
    const message = status === "VERIFYING"
      ? "¿Cancelar la verificación? Los archivos ya copiados se conservarán, pero no quedarán verificados."
      : "¿Cancelar la copia? Se eliminará lo que se haya copiado hasta ahora.";
    if (!confirm(message)) return;

    btn.disabled = true;
    try {
      const res = await fetch(`/api/v1/ingests/${id}/cancel`, { method: "POST" });
      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        alert("No se ha podido cancelar: " + (err.error || res.statusText));
        btn.disabled = false;
      }
    } catch (e) {
      alert("Error de red al cancelar: " + e.message);
      btn.disabled = false;
    }
  }

  async function doRetry(id, btn) {
    btn.disabled = true;
    try {
      const res = await fetch(`/api/v1/ingests/${id}/retry`, { method: "POST" });
      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        alert("No se ha podido reintentar: " + (err.error || res.statusText));
        btn.disabled = false;
      }
    } catch (e) {
      alert("Error de red al reintentar: " + e.message);
      btn.disabled = false;
    }
  }

  async function pollHealth() {
    try {
      const res = await fetch("/health");
      healthDot.className = "dot " + (res.ok ? "ok" : "down");
      healthLabel.textContent = "NAS: " + (res.ok ? "OK" : "ERROR");
    } catch {
      healthDot.className = "dot down";
      healthLabel.textContent = "NAS: SIN CONEXIÓN";
    }
  }

  async function pollStatus() {
    try {
      const res = await fetch("/api/v1/status");
      const status = await res.json();
      const percent = status.storage ? status.storage.percent_used : 0;
      const low = percent >= STORAGE_WARN_PERCENT;
      storageDot.className = "dot " + (low ? "down" : "ok");
      storageLabel.textContent = "Almacenamiento: " + (low ? "BAJO" : "OK");
      projectNameEl.textContent = "Proyecto: " + (status.active_event || "");
    } catch {
      storageDot.className = "dot down";
      storageLabel.textContent = "Almacenamiento: SIN DATOS";
    }
  }

  async function init() {
    await loadConfig();
    await loadIngests();
    connectEvents();
    pollHealth();
    pollStatus();
    setInterval(pollHealth, 5000);
    setInterval(pollStatus, 5000);
  }

  init();
})();
