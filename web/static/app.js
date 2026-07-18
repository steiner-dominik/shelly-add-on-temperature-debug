"use strict";
// Shelly Add-on Temperature Debug frontend. No frameworks, no external
// resources; served with CSP script-src/style-src 'self', so no inline
// style="" attributes may appear in generated markup (colors use classes
// and SVG presentation attributes instead).
const base = location.pathname.replace(/\/$/, "");
const $ = id => document.getElementById(id);
const SERIES = [1, 2, 3, 4, 5, 6, 7, 8].map(i => `var(--series-${i})`);
const esc = s => String(s).replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const KINDS = ["temperature", "humidity"];
const UNIT = { temperature: " °C", humidity: " %" };
const AXIS_UNIT = { temperature: "°", humidity: "%" };

// --- i18n -------------------------------------------------------------
let L = null;         // active locale object
let locales = [];     // [{code, name}]
const fmt = (s, vars) => String(s || "").replace(/\{(\w+)\}/g, (_, k) => vars && k in vars ? vars[k] : "{" + k + "}");
const t = key => (L && L.ui && L.ui[key]) || key;

async function loadLocales() {
  locales = await (await fetch(base + "/locales/index.json")).json();
  let lang = localStorage.getItem("lang");
  if (!locales.some(l => l.code === lang)) {
    const nav = (navigator.language || "en").slice(0, 2).toLowerCase();
    lang = locales.some(l => l.code === nav) ? nav : "en";
  }
  L = await (await fetch(base + "/locales/" + lang + ".json")).json();
  document.documentElement.lang = lang;
  const sel = $("langsel");
  sel.innerHTML = locales.map(l => `<option value="${esc(l.code)}"${l.code === lang ? " selected" : ""}>${esc(l.name)}</option>`).join("");
  sel.onchange = async () => {
    localStorage.setItem("lang", sel.value);
    L = await (await fetch(base + "/locales/" + sel.value + ".json")).json();
    document.documentElement.lang = sel.value;
    applyChrome();
    if (lastData) {
      render(lastData, lastHist);
      $("lastq").textContent = fmt(t("lastQuery"), { time: new Date(lastData.ts * 1000).toLocaleTimeString() });
    }
  };
}

function applyChrome() {
  $("subtitle").textContent = t("subtitle");
  $("search").placeholder = t("searchPlaceholder");
  $("querybtn").textContent = t("queryBtn");
  updateWiggleBtn();
  $("wigglehint").textContent = t("wiggleHint");
  $("autolabel").textContent = fmt(t("autoRefresh"), { s: autoRefreshSec });
  $("csvbtn").textContent = t("downloadCsv");
  $("clearbtn").textContent = t("clearHistory");
  $("tokenprompt").textContent = t("tokenPrompt");
  $("tokeninput").placeholder = t("tokenPlaceholder");
  $("tokensave").textContent = t("unlock");
  $("historynote").textContent = t("historyNote");
  $("disclaimer").textContent = t("disclaimer");
  const lg = [["ok", "legendOk"], ["reset85", "legendReset85"], ["read_error", "legendReadError"], ["missing", "legendMissing"]]
    .map(([st, key]) => `<b>${esc(L.status[st])}</b> — ${esc(t(key))}`).join(" · ");
  $("legendline").innerHTML = `${esc(t("legendTitle"))} ${lg}`;
  const th = $("themesel");
  const cur = localStorage.getItem("theme") || "auto";
  th.innerHTML = [["auto", "themeAuto"], ["light", "themeLight"], ["dark", "themeDark"]]
    .map(([v, key]) => `<option value="${v}"${v === cur ? " selected" : ""}>${esc(t(key))}</option>`).join("");
}

// --- sticky toolbar offset --------------------------------------------
// The toolbar sticks right below the top bar; its height varies with
// wrapping (mobile) and is mirrored into --topbar-h for the CSS.
{
  const topbar = document.querySelector(".topbar");
  const setTopbarH = () =>
    document.documentElement.style.setProperty("--topbar-h", topbar.offsetHeight + "px");
  new ResizeObserver(setTopbarH).observe(topbar);
  setTopbarH();
}

// --- theme ------------------------------------------------------------
function applyTheme(mode) {
  if (mode === "light" || mode === "dark") document.documentElement.dataset.theme = mode;
  else delete document.documentElement.dataset.theme;
}
applyTheme(localStorage.getItem("theme") || "auto");
$("themesel").addEventListener("change", e => {
  localStorage.setItem("theme", e.target.value);
  applyTheme(e.target.value);
});

// --- auth: mandatory token, kept in localStorage across reloads --------
function authHeaders() {
  const tok = localStorage.getItem("debugToken");
  return tok ? { "X-Debug-Token": tok } : {};
}
function showLocked(rejected) {
  stopWiggle();
  $("loginbox").hidden = false;
  $("toolbar").hidden = true;
  $("statusstrip").hidden = true;
  $("results").hidden = true;
  $("banner").hidden = true;
  $("loginerr").hidden = !rejected;
  if (rejected) $("loginerr").textContent = t("tokenRejected");
  $("tokeninput").focus();
}
function showUnlocked() {
  $("loginbox").hidden = true;
  $("toolbar").hidden = false;
  $("results").hidden = false;
}
$("tokensave").addEventListener("click", () => {
  const v = $("tokeninput").value.trim();
  if (!v) return;
  localStorage.setItem("debugToken", v);
  $("tokeninput").value = "";
  showUnlocked();
  runQuery();
});
$("tokeninput").addEventListener("keydown", e => { if (e.key === "Enter") $("tokensave").click(); });

// --- API --------------------------------------------------------------
async function api(path, opts = {}) {
  const resp = await fetch(base + path, { ...opts, headers: { ...authHeaders(), ...(opts.headers || {}) } });
  if (resp.status === 401) { throw { unauthorized: true }; }
  if (!resp.ok) throw new Error("HTTP " + resp.status);
  return resp.json();
}

// --- connection state --------------------------------------------------
function setLive(state) { $("livedot").className = "live-dot " + state; }
let bannerTimer = null;
function showBanner(text, autohideMs) {
  $("bannertext").textContent = text;
  $("banner").hidden = false;
  if (bannerTimer) { clearTimeout(bannerTimer); bannerTimer = null; }
  if (autohideMs) bannerTimer = setTimeout(() => { $("banner").hidden = true; }, autohideMs);
}
function hideBanner() {
  if (bannerTimer) { clearTimeout(bannerTimer); bannerTimer = null; }
  $("banner").hidden = true;
}

// --- querying ---------------------------------------------------------
let lastData = null, lastHist = null, queryBusy = false;
let minIntervalMs = 2000;  // reported by the server with every query
let autoRefreshSec = 30;   // ditto (AUTO_REFRESH_SECONDS)

// Charts fetch at most this many samples per sensor; the full buffer
// (HISTORY_SIZE) is only pulled for the CSV export.
const CHART_HISTORY_LIMIT = 1000;

async function runQuery() {
  if (queryBusy) return;
  queryBusy = true;
  const btn = $("querybtn");
  btn.disabled = true; btn.textContent = t("querying");
  try {
    const data = await api("/api/query", { method: "POST" });
    const hist = await api("/api/history?limit=" + CHART_HISTORY_LIMIT);
    if (data.minIntervalSec != null) minIntervalMs = data.minIntervalSec * 1000;
    applyServerRefreshConfig(data);
    lastData = data; lastHist = hist;
    showUnlocked();
    render(data, hist);
    $("lastq").textContent = fmt(t("lastQuery"), { time: new Date(data.ts * 1000).toLocaleTimeString() });
    setLive("ok");
    hideBanner();
  } catch (e) {
    if (e && e.unauthorized) {
      showLocked(localStorage.getItem("debugToken") != null);
    } else {
      setLive("down");
      // Keep the last known data on screen; just flag the lost connection.
      if (lastData) {
        showBanner(t("disconnected"));
      } else {
        $("results").innerHTML = `<p class="empty">${esc(fmt(t("queryFailed"), { err: e.message || e }))}</p>`;
      }
    }
  } finally {
    queryBusy = false;
    btn.disabled = false; btn.textContent = t("queryBtn");
  }
}
$("querybtn").addEventListener("click", runQuery);

// --- per-sensor query --------------------------------------------------
$("results").addEventListener("click", async e => {
  const b = e.target.closest("button.srefresh");
  if (!b || b.disabled || !lastData) return;
  b.disabled = true; b.classList.add("busy");
  try {
    const resp = await api(`/api/query/sensor?ep=${encodeURIComponent(b.dataset.ep)}&key=${encodeURIComponent(b.dataset.key)}`, { method: "POST" });
    const ep = lastData.endpoints[Number(b.dataset.ep)];
    if (ep) {
      const i = ep.sensors.findIndex(s => s.key === resp.sensor.key);
      if (i >= 0) ep.sensors[i] = resp.sensor; else ep.sensors.push(resp.sensor);
    }
    lastHist = await api("/api/history?limit=" + CHART_HISTORY_LIMIT);
    render(lastData, lastHist);
    setLive("ok");
  } catch (err) {
    if (err && err.unauthorized) showLocked(true);
    else showBanner(fmt(t("sensorQueryFailed"), { err: err.message || err }), 5000);
    b.disabled = false; b.classList.remove("busy");
  }
});

// --- clear history -----------------------------------------------------
$("clearbtn").addEventListener("click", async () => {
  if (!confirm(t("clearHistoryConfirm"))) return;
  try {
    await api("/api/history", { method: "DELETE" });
    lastHist = await api("/api/history?limit=" + CHART_HISTORY_LIMIT);
    if (lastData) render(lastData, lastHist);
  } catch (e) {
    if (e && e.unauthorized) showLocked(true);
    else showBanner(fmt(t("queryFailed"), { err: e.message || e }), 5000);
  }
});

// --- CSV export: the full in-memory buffer, not just the chart window ------
$("csvbtn").addEventListener("click", async () => {
  const btn = $("csvbtn");
  btn.disabled = true;
  try {
    const hist = await api("/api/history");
    const rows = [["endpoint", "sensor_key", "sensor_name", "kind", "time_iso", "value", "status"]];
    Object.entries(hist.endpoints || {}).forEach(([epName, byKey]) => {
      Object.entries(byKey).forEach(([key, sh]) => {
        (sh.samples || []).forEach(sm => rows.push([
          epName, key, sh.name, sh.kind,
          new Date(sm.ts * 1000).toISOString(), sm.v == null ? "" : sm.v, sm.status,
        ]));
      });
    });
    const csv = rows.map(r => r.map(f => `"${String(f).replace(/"/g, '""')}"`).join(",")).join("\r\n");
    const url = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "shelly-debug-history-" + new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19) + ".csv";
    a.click();
    URL.revokeObjectURL(url);
  } catch (e) {
    if (e && e.unauthorized) showLocked(true);
    else showBanner(fmt(t("queryFailed"), { err: e.message || e }), 5000);
  } finally {
    btn.disabled = false;
  }
});

// --- search / filter ---------------------------------------------------
let filter = "";
function epMatches(ep) {
  if (!filter) return true;
  const hay = [ep.name, ep.host, ep.device || ""]
    .concat(ep.sensors.flatMap(s => [s.name, s.key])).join("\n").toLowerCase();
  return hay.includes(filter);
}
$("search").addEventListener("input", e => {
  filter = e.target.value.trim().toLowerCase();
  if (lastData) render(lastData, lastHist);
});
document.addEventListener("keydown", e => {
  if (e.key === "/" && !e.ctrlKey && !e.metaKey && !e.altKey
    && !/^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName)) {
    e.preventDefault();
    $("search").focus();
  }
});
$("devchips").addEventListener("click", e => {
  const chip = e.target.closest(".devchip");
  if (!chip || !lastData) return;
  const name = chip.dataset.name;
  const next = filter === name.toLowerCase() ? "" : name;
  $("search").value = next;
  filter = next.toLowerCase();
  render(lastData, lastHist);
});

// --- wiggle mode: poll rapidly for 60 s while cables are re-seated -----
// The step stays above the server's QUERY_MIN_INTERVAL_SECONDS so every
// poll is a real device query, not a cached result.
const WIGGLE_MS = 60000;
const wiggleStep = () => Math.max(2500, minIntervalMs + 500);
let wiggleTimer = null, wiggleUntil = 0;
function updateWiggleBtn() {
  const btn = $("wigglebtn");
  if (wiggleTimer) {
    btn.textContent = fmt(t("wiggleRunning"), { s: Math.max(0, Math.ceil((wiggleUntil - Date.now()) / 1000)) });
  } else {
    btn.textContent = t("wiggleBtn");
  }
}
function startWiggle() {
  wiggleUntil = Date.now() + WIGGLE_MS;
  $("wigglebtn").classList.add("active");
  $("wigglehint").hidden = false;
  wiggleTimer = setInterval(() => {
    if (Date.now() >= wiggleUntil) { stopWiggle(); return; }
    updateWiggleBtn();
    runQuery();
  }, wiggleStep());
  updateWiggleBtn();
  runQuery();
}
function stopWiggle() {
  if (wiggleTimer) clearInterval(wiggleTimer);
  wiggleTimer = null;
  $("wigglebtn").classList.remove("active");
  $("wigglehint").hidden = true;
  updateWiggleBtn();
}
$("wigglebtn").addEventListener("click", () => wiggleTimer ? stopWiggle() : startWiggle());

// --- auto-refresh, paused while the tab is hidden or wiggling ------------
// The interval (AUTO_REFRESH_SECONDS) and whether it starts enabled
// (AUTO_REFRESH_DEFAULT) come from the server with every query response.
// A user toggle is persisted and wins over the server default.
let autoTimer = null;
function armAutoRefresh(on) {
  if (autoTimer) { clearInterval(autoTimer); autoTimer = null; }
  if (on) autoTimer = setInterval(() => { if (!document.hidden && !wiggleTimer && $("loginbox").hidden) runQuery(); }, autoRefreshSec * 1000);
}
function setAutoRefresh(on) {
  localStorage.setItem("autoRefresh", on ? "1" : "0");
  armAutoRefresh(on);
}
function applyServerRefreshConfig(data) {
  if (data.autoRefreshSec != null && data.autoRefreshSec !== autoRefreshSec) {
    autoRefreshSec = data.autoRefreshSec;
    $("autolabel").textContent = fmt(t("autoRefresh"), { s: autoRefreshSec });
    if (autoTimer) armAutoRefresh(true); // re-arm with the new interval
  }
  // Server default applies only while the user has never touched the toggle;
  // it is deliberately not persisted so the env var stays in control.
  if (data.autoRefreshDefault && localStorage.getItem("autoRefresh") == null && !$("autorefresh").checked) {
    $("autorefresh").checked = true;
    armAutoRefresh(true);
  }
}
$("autorefresh").checked = localStorage.getItem("autoRefresh") === "1";
$("autorefresh").addEventListener("change", e => setAutoRefresh(e.target.checked));
armAutoRefresh($("autorefresh").checked);

// --- rendering --------------------------------------------------------
const CHIP = {
  ok: "ok", reset85: "warn", read_error: "crit", missing: "crit",
  unreachable: "crit", auth_failed: "crit", no_sensors: "warn",
};
const SEV = { ok: 0, warn: 1, crit: 2 };
const chip = st => `<span class="chip ${CHIP[st] || "warn"}">${esc(L.status[st] || st)}</span>`;
const fmtV = (v, kind) => v == null ? "—" : v.toFixed(1) + (UNIT[kind] || "");
const kindLabel = kind => kind === "humidity" ? t("kindHumidity") : t("kindTemperature");

// Stable per-endpoint colors: sensors are numbered within their kind so each
// chart starts at series-1 and the table dots match the chart lines.
// Values are indices 0..7: class "c<i>" for dots, SERIES[i] for SVG strokes.
function colorMap(sensors) {
  const m = {}, count = {};
  sensors.forEach(s => {
    const i = count[s.kind] || 0;
    m[s.key] = i % 8;
    count[s.kind] = i + 1;
  });
  return m;
}

// Overall state of one endpoint incl. its sensors: worst of everything.
function epSeverity(ep) {
  let worst = CHIP[ep.status] || "warn";
  ep.sensors.forEach(s => {
    const c = CHIP[s.status] || "warn";
    if (SEV[c] > SEV[worst]) worst = c;
  });
  return worst;
}

function renderStrip(data) {
  const eps = data.endpoints;
  const devOk = eps.filter(e => e.status === "ok").length;
  const allSensors = eps.flatMap(e => e.sensors);
  const senOk = allSensors.filter(s => s.status === "ok").length;
  const problems = (eps.length - devOk) + (allSensors.length - senOk);
  $("aggstats").innerHTML = [
    { v: `${devOk}/${eps.length}`, l: t("tileDevices"), cls: devOk === eps.length ? "good" : "bad" },
    { v: `${senOk}/${allSensors.length}`, l: t("tileSensors"), cls: senOk === allSensors.length ? "good" : "bad" },
    { v: String(problems), l: t("tileProblems"), cls: problems === 0 ? "good" : "bad" },
  ].map(x => `<span class="agg-item ${x.cls}"><b>${esc(x.v)}</b> ${esc(x.l)}</span>`).join("");
  $("devchips").innerHTML = eps.map(ep => {
    const ok = ep.sensors.filter(s => s.status === "ok").length;
    const sev = epSeverity(ep);
    const active = filter && filter === ep.name.toLowerCase();
    const dim = filter && !epMatches(ep);
    return `<button class="devchip${active ? " active" : ""}${dim ? " dim" : ""}" data-name="${esc(ep.name)}" type="button">
      <span class="dot-s ${sev}"></span>${esc(ep.name)}<span class="cnt">${ok}/${ep.sensors.length}</span></button>`;
  }).join("");
  $("statusstrip").hidden = false;
}

// --- trend vs. ~5 minutes ago ------------------------------------------
// Baseline: the recorded sample closest to 5 min before the newest reading,
// but at least 1 min older — with less history than that, no arrow is shown.
const TREND_WINDOW = 300, TREND_MIN_AGE = 60;
const TREND_EPS = { temperature: 0.3, humidity: 1.0 }; // dead band per kind
const TREND_ARROW = { up: "↗", down: "↘", flat: "→" };
function trendFor(samples, kind) {
  if (!samples || samples.length < 2) return null;
  let last = null;
  for (let i = samples.length - 1; i >= 0; i--) {
    if (samples[i].v != null) { last = samples[i]; break; }
  }
  if (!last) return null;
  const target = last.ts - TREND_WINDOW;
  let base = null;
  for (const sm of samples) {
    if (sm.v == null || sm.ts > last.ts - TREND_MIN_AGE) continue;
    if (!base || Math.abs(sm.ts - target) < Math.abs(base.ts - target)) base = sm;
  }
  if (!base) return null;
  const delta = last.v - base.v;
  const eps = TREND_EPS[kind] != null ? TREND_EPS[kind] : 0.3;
  const dir = delta > eps ? "up" : delta < -eps ? "down" : "flat";
  const mins = Math.max(1, Math.round((last.ts - base.ts) / 60));
  const title = `${delta >= 0 ? "+" : ""}${delta.toFixed(1)}${UNIT[kind] || ""} / ${mins} min`;
  return { dir, delta, title };
}

function render(data, hist) {
  renderStrip(data);
  const out = [];
  data.endpoints.forEach((ep, epIdx) => {
    if (!epMatches(ep)) return;
    const colors = colorMap(ep.sensors);
    const kindsPresent = KINDS.filter(k => ep.sensors.some(s => s.kind === k));
    const meta = [ep.device, ep.wifiRssi != null ? `${t("wifi")} ${ep.wifiRssi} dBm` : null,
      ep.uptimeSec != null ? `${t("uptime")} ${fmtUptime(ep.uptimeSec)}` : null].filter(Boolean).join(" · ");
    let body = "";
    if (ep.status !== "ok") {
      const g = (L.guidance.endpoint || {})[ep.status] || "";
      body += `<div class="guide ${CHIP[ep.status] || "warn"}">${esc(g)}${ep.error ? `<br><span class="skey">${esc(ep.error)}</span>` : ""}</div>`;
    }
    if (ep.sensors.length) {
      const valueHeader = kindsPresent.length > 1 ? t("colValue") : kindLabel(kindsPresent[0]);
      body += `<table class="sensors"><tr><th>${esc(t("colSensor"))}</th><th>${esc(valueHeader)}</th><th>${esc(t("colStatus"))}</th><th></th></tr>`;
      ep.sensors.forEach(s => {
        const tr = trendFor((((hist.endpoints || {})[ep.name] || {})[s.key] || {}).samples, s.kind);
        const trendHtml = tr
          ? `<span class="trend ${tr.dir}" title="${esc(tr.title)}" aria-label="${esc(tr.title)}">${TREND_ARROW[tr.dir]}</span>` : "";
        // <wbr> after the colon lets narrow screens break "temperature:100"
        // cleanly between kind and id instead of overflowing the card.
        body += `<tr>
          <td><span class="dot c${colors[s.key]}"></span><span class="sname">${esc(s.name)}</span><br><span class="skey">${esc(s.key).replace(":", ":<wbr>")}</span></td>
          <td><span class="temp ${s.value == null ? "na" : ""}">${fmtV(s.value, s.kind)}</span>${trendHtml}</td>
          <td>${chip(s.status)}</td>
          <td class="actions"><button class="srefresh" type="button" data-ep="${epIdx}" data-key="${esc(s.key)}" title="${esc(t("querySensor"))}" aria-label="${esc(t("querySensor"))}">↻</button></td></tr>`;
        const g = (L.guidance.sensor || {})[s.status];
        if (g) body += `<tr><td colspan="4" class="guiderow"><div class="guide ${CHIP[s.status]}">${esc(g)}</div></td></tr>`;
      });
      body += `</table>`;
    }
    if (ep.wifiRssi != null && ep.wifiRssi <= -75) {
      body += `<p class="note">ℹ️ ${esc(fmt(t("wifiWeak"), { rssi: ep.wifiRssi }))}</p>`;
    }
    kindsPresent.forEach(kind => {
      body += chartFor(ep, (hist.endpoints || {})[ep.name] || {}, epIdx, kind, colors, kindsPresent.length > 1);
    });
    out.push(`<div class="card st-${epSeverity(ep)}">
      <div class="ephead"><h2>${esc(ep.name)}</h2><span class="ephost">${esc(ep.host)}</span>${chip(ep.status)}</div>
      ${meta ? `<div class="epmeta">${esc(meta)}</div>` : ""}${body}</div>`);
  });
  if (!out.length) out.push(`<p class="empty">${esc(t("noMatches"))}</p>`);
  $("results").innerHTML = out.join("");
  bindTooltips();
}

const fmtUptime = s => s >= 86400 ? Math.floor(s / 86400) + "d " + Math.floor(s % 86400 / 3600) + "h"
  : s >= 3600 ? Math.floor(s / 3600) + "h " + Math.floor(s % 3600 / 60) + "m" : Math.floor(s / 60) + "m";
const fmtClock = ts => new Date(ts * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });

// One line chart per endpoint and sensor kind: x = query time, y = value.
// Failed reads (v null) break the line and are marked with an ✕ under the plot.
const chartData = {}; // chartId -> {tsList, series} for tooltip lookup
function chartFor(ep, histSensors, epIdx, kind, colors, titledKind) {
  const sensors = ep.sensors.filter(s => s.kind === kind && histSensors[s.key]);
  const series = sensors.map(s => ({
    key: s.key, name: histSensors[s.key].name, kind, ci: colors[s.key],
    byTs: new Map(histSensors[s.key].samples.map(sm => [sm.ts, sm])),
  }));
  const tsSet = new Set();
  series.forEach(s => s.byTs.forEach((_, ts) => tsSet.add(ts)));
  const tsList = [...tsSet].sort((a, b) => a - b);
  if (!tsList.length) return "";
  const chartId = epIdx + ":" + kind;
  chartData[chartId] = { tsList, series };

  // Keep the SVG light with long histories: draw at most MAX_POINTS columns
  // (strided, always keeping the newest) and drop the per-point circles once
  // they would just smear into the line. Tooltips still see every sample.
  const MAX_POINTS = 600, MAX_CIRCLES = 240;
  let plotTs = tsList;
  if (plotTs.length > MAX_POINTS) {
    const stride = Math.ceil(plotTs.length / MAX_POINTS);
    plotTs = plotTs.filter((_, i) => i % stride === 0 || i === tsList.length - 1);
  }

  const W = 820, H = 180, padL = 44, padR = 10, padT = 10, padB = 26;
  const values = [];
  series.forEach(s => s.byTs.forEach(sm => { if (sm.v != null) values.push(sm.v); }));
  let lo = values.length ? Math.min(...values) : 0, hi = values.length ? Math.max(...values) : 1;
  if (hi - lo < 2) { const m = (hi + lo) / 2; lo = m - 1; hi = m + 1; }
  const span = hi - lo; lo -= span * 0.08; hi += span * 0.08;
  const t0 = tsList[0], t1 = tsList[tsList.length - 1];
  const x = ts => t1 === t0 ? (padL + (W - padL - padR) / 2) : padL + (ts - t0) / (t1 - t0) * (W - padL - padR);
  const y = v => padT + (hi - v) / (hi - lo) * (H - padT - padB);

  let g = "";
  for (let i = 0; i <= 2; i++) {
    const v = lo + (hi - lo) * i / 2, yy = y(v);
    g += `<line x1="${padL}" y1="${yy}" x2="${W - padR}" y2="${yy}" stroke="var(--grid)" stroke-width="1"/>`;
    g += `<text x="${padL - 6}" y="${yy + 4}" text-anchor="end">${v.toFixed(1)}${AXIS_UNIT[kind] || ""}</text>`;
  }
  g += `<text x="${padL}" y="${H - 8}">${fmtClock(t0)}</text>`;
  if (t1 !== t0) g += `<text x="${W - padR}" y="${H - 8}" text-anchor="end">${fmtClock(t1)}</text>`;

  series.forEach(s => {
    const color = SERIES[s.ci];
    let path = "", pen = false;
    plotTs.forEach(ts => {
      const sm = s.byTs.get(ts);
      if (sm && sm.v != null) {
        path += (pen ? "L" : "M") + x(ts).toFixed(1) + " " + y(sm.v).toFixed(1) + " ";
        pen = true;
      } else if (sm) {
        // failed read: break the line, mark the failure
        g += `<text class="xmark" x="${x(ts).toFixed(1)}" y="${H - padB - 4}" text-anchor="middle">✕</text>`;
        pen = false;
      } else { pen = false; }
    });
    if (path) g += `<path d="${path}" fill="none" stroke="${color}" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`;
    if (plotTs.length <= MAX_CIRCLES) {
      plotTs.forEach(ts => {
        const sm = s.byTs.get(ts);
        if (sm && sm.v != null) g += `<circle cx="${x(ts).toFixed(1)}" cy="${y(sm.v).toFixed(1)}" r="2.5" fill="${color}"/>`;
      });
    }
  });

  const legend = series.map(s => {
    const last = [...s.byTs.values()].pop();
    return `<span><span class="dot c${s.ci}"></span>${esc(s.name)}&nbsp;<b>${fmtV(last ? last.v : null, kind)}</b></span>`;
  }).join("");

  let title = fmt(t("history"), { n: tsList.length, queries: tsList.length === 1 ? t("queryOne") : t("queryOther") });
  if (titledKind) title = kindLabel(kind) + " · " + title;
  return `<div class="chartwrap"><div class="charttitle">${esc(title)}</div>
    <svg class="chart" viewBox="0 0 ${W} ${H}" data-chart="${esc(chartId)}" preserveAspectRatio="none">
      <line x1="${padL}" y1="${H - padB}" x2="${W - padR}" y2="${H - padB}" stroke="var(--baseline)" stroke-width="1"/>
      ${g}
      <line class="xh" x1="0" y1="${padT}" x2="0" y2="${H - padB}" stroke="var(--baseline)" stroke-width="1"/>
    </svg><div class="legend">${legend}</div></div>`;
}

// crosshair + tooltip: nearest query time under the cursor, all sensors listed
function bindTooltips() {
  const tt = $("tooltip");
  document.querySelectorAll("svg.chart").forEach(svg => {
    const { tsList, series } = chartData[svg.dataset.chart] || {};
    if (!tsList) return;
    const W = 820, padL = 44, padR = 10;
    const t0 = tsList[0], t1 = tsList[tsList.length - 1];
    svg.addEventListener("mousemove", e => {
      const r = svg.getBoundingClientRect();
      const vx = (e.clientX - r.left) / r.width * W;
      const frac = t1 === t0 ? 0 : Math.min(1, Math.max(0, (vx - padL) / (W - padL - padR)));
      const target = t0 + frac * (t1 - t0);
      let best = tsList[0];
      tsList.forEach(ts => { if (Math.abs(ts - target) < Math.abs(best - target)) best = ts; });
      const xh = svg.querySelector(".xh");
      const xPos = t1 === t0 ? padL + (W - padL - padR) / 2 : padL + (best - t0) / (t1 - t0) * (W - padL - padR);
      xh.setAttribute("x1", xPos); xh.setAttribute("x2", xPos); xh.style.display = "inline";
      let rows = "";
      series.forEach(s => {
        const sm = s.byTs.get(best);
        if (!sm) return;
        rows += `<div class="tt-row"><span class="dot c${s.ci}"></span>${esc(s.name)}<span class="tt-val">${sm.v == null ? esc(L.status[sm.status] || "—") : fmtV(sm.v, s.kind)}</span></div>`;
      });
      tt.innerHTML = `<div class="tt-time">${fmtClock(best)}</div>` + rows;
      tt.style.display = "block";
      const tw = tt.offsetWidth;
      tt.style.left = Math.min(e.clientX + 14, window.innerWidth - tw - 8) + "px";
      tt.style.top = (e.clientY + 14) + "px";
    });
    svg.addEventListener("mouseleave", () => {
      tt.style.display = "none";
      svg.querySelector(".xh").style.display = "none";
    });
  });
}

// --- boot -------------------------------------------------------------
// PWA: relative path keeps the scope correct under any BASE_PATH (and
// behind Home Assistant ingress).
if ("serviceWorker" in navigator) navigator.serviceWorker.register("sw.js").catch(() => {});
(async () => {
  try {
    await loadLocales();
  } catch (e) {
    L = { ui: {}, status: {}, guidance: { sensor: {}, endpoint: {} } };
  }
  applyChrome();
  // Probe with whatever token is stored (possibly none): if the server has
  // auth disabled (DEBUG_TOKEN="") this succeeds straight away, otherwise a
  // 401 brings up the login card.
  runQuery();
})();
