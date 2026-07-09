"use strict";

// Ranges up to LIVE_MAX_MIN minutes render the raw 250ms live samples
// (/api/live); longer ranges render per-minute aggregates (/api/history).
const LIVE_MAX_MIN = 10;
let rangeMinutes = 24 * 60;
let refreshTimer = null;
// Rendered view: chart instances are created once and updated via setData;
// they're rebuilt only when this signature (mode + targets + has-data) changes.
let view = { sig: null, cards: new Map() }; // target -> { rtt, jit } chart handles

const css = name =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim();

function palette() {
  return {
    rtt: css("--series-rtt"),
    jitter: css("--series-jitter"),
    envelope: css("--envelope"),
    loss: css("--loss"),
    grid: css("--grid"),
    muted: css("--muted"),
  };
}

function hexToRgba(hex, a) {
  let h = hex.replace("#", "");
  if (h.length === 3) h = h.split("").map(ch => ch + ch).join("");
  const n = parseInt(h, 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${a})`;
}

async function fetchJSON(url) {
  const r = await fetch(url);
  if (!r.ok) throw new Error(`${url}: ${r.status}`);
  return r.json();
}

// A minute with 0 or 1 successful samples can't yield RTT/jitter we trust
// (jitter needs 2+ successes for a delta). It's "faulty": not plotted as data,
// drawn as a red rectangle instead.
const faulty = r => (r.samples - r.lost) <= 1;

const fmtMS = (u, v) => v == null ? "–" : `${v.toFixed(2)} ms`;
const fmtPct = (u, v) => v == null ? "–" : `${v.toFixed(1)} %`;

// Column-major per-minute data for uPlot; null = gap. Missing minutes (collector
// was down) get an explicit null row so lines don't bridge the hole. Faulty
// minutes are nulled in every series and flagged in `flt` for the red overlay.
function toColumns(rows) {
  const d = { xs: [], avg: [], p99: [], jP10: [], jP50: [], jP99: [], jMax: [], loss: [], flt: [] };
  let prev = null;
  const pushNulls = t => {
    d.xs.push(t / 1000);
    for (const k of ["avg", "p99", "jP10", "jP50", "jP99", "jMax", "loss"]) d[k].push(null);
    d.flt.push(false);
  };
  for (const r of rows) {
    if (prev != null && r._t - prev > 90_000) pushNulls(prev + 60_000);
    prev = r._t;
    const bad = faulty(r);
    d.xs.push(r._t / 1000);
    d.avg.push(bad ? null : r.avg_ms);
    d.p99.push(bad ? null : r.p99_ms);
    d.jP10.push(bad ? null : r.jitter_p10_ms);
    d.jP50.push(bad ? null : r.jitter_p50_ms);
    d.jP99.push(bad ? null : r.jitter_p99_ms);
    d.jMax.push(bad ? null : r.jitter_max_ms);
    d.loss.push(bad ? null : r.loss_pct);
    d.flt.push(bad);
  }
  return d;
}

// Column-major live data. Instantaneous jitter = |Δrtt| between successive
// successful samples (a loss breaks the drawn line but, as on the backend,
// does not reset the chain — the next success is compared to the last one).
function toLiveColumns(pts) {
  const d = { xs: [], rtt: [], jit: [], lost: [] };
  let lastRtt = null;
  for (const p of pts) {
    d.xs.push(p._t / 1000);
    if (p.lost) {
      d.rtt.push(null);
      d.jit.push(null);
      d.lost.push(1);
    } else {
      d.rtt.push(p.rtt_ms);
      d.jit.push(lastRtt == null ? null : Math.abs(p.rtt_ms - lastRtt));
      d.lost.push(0);
      lastRtt = p.rtt_ms;
    }
  }
  return d;
}

const yRange = (u, min, max) => [0, Math.max(max || 0, 1) * 1.1];

// Pin the x-axis to the selected window [now - range, now] rather than letting
// uPlot auto-fit the data extent. Otherwise, before the buffer/history covers
// the range (e.g. just after startup), 3m/5m/10m all fit the same few minutes
// of data and render identically. A fixed window makes each range visibly its
// own width, with whatever data exists sitting at the right.
function xScale() {
  return {
    time: true,
    range: () => {
      const now = Date.now() / 1000;
      return [now - rangeMinutes * 60, now];
    },
  };
}

function axes(c) {
  const base = {
    stroke: c.muted,
    font: "11px system-ui, -apple-system, sans-serif",
    grid: { stroke: c.grid, width: 1 },
    ticks: { stroke: c.grid, width: 1, size: 4 },
  };
  return [
    { ...base, grid: { show: false } },   // x: no vertical gridlines
    { ...base, size: 52 },                // y
  ];
}

// ---- Draw hooks ----
// Hooks read the chart's current data through a mutable `state.d`, refreshed on
// every setData, so overlays repaint with new data without rebuilding the chart.

// Translucent red rectangle over each faulty minute. Centred on the minute's
// point (±30s), clamped to the plot with a 2px floor so the newest faulty
// minute (right edge) and zoomed-out ones stay visible.
const faultyBands = state => u => {
  const d = state.d;
  if (!d || !d.flt) return;
  const ctx = u.ctx, left = u.bbox.left, right = u.bbox.left + u.bbox.width;
  ctx.save();
  ctx.fillStyle = hexToRgba(css("--loss"), 0.22);
  for (let i = 0; i < d.flt.length; i++) {
    if (!d.flt[i]) continue;
    const x0 = Math.max(u.valToPos(d.xs[i] - 30, "x", true), left);
    const x1 = Math.min(u.valToPos(d.xs[i] + 30, "x", true), right);
    if (!(x1 > x0)) continue; // fully off-plot or NaN scale
    const w = Math.max(x1 - x0, 2);
    const x = Math.max(Math.min(x0, right - w), left);
    ctx.fillRect(x, u.bbox.top, w, u.bbox.height);
  }
  ctx.restore();
};

// Red baseline dots for loss. key="loss" (per-minute history, skips faulty
// minutes which already show a red band) or key="lost" (per-sample live).
const lossDots = (state, key) => u => {
  const d = state.d;
  if (!d || !d[key]) return;
  const ctx = u.ctx, dpr = devicePixelRatio || 1, y = u.bbox.top + u.bbox.height - 4 * dpr;
  ctx.save();
  ctx.fillStyle = css("--loss");
  for (let i = 0; i < d[key].length; i++) {
    const show = key === "loss" ? (d[key][i] > 0 && !d.flt[i]) : d[key][i];
    if (!show) continue;
    ctx.beginPath();
    ctx.arc(u.valToPos(d.xs[i], "x", true), y, 2.5 * dpr, 0, 2 * Math.PI);
    ctx.fill();
  }
  ctx.restore();
};

// ---- Chart builders: each returns { u, state, cols } ----
// `cols(d)` maps a data object to the column array setData expects, so updates
// are mode-agnostic.

function rttChart(el, d, c) {
  const state = { d };
  const u = new uPlot({
    width: el.clientWidth, height: 190,
    cursor: { sync: { key: "jitter" } },
    scales: { x: xScale(), y: { range: yRange }, loss: { range: [0, 100] } },
    axes: axes(c),
    series: [
      {},
      { label: "avg RTT", stroke: c.rtt, width: 2, value: fmtMS },
      { label: "p99", stroke: c.envelope, width: 1, value: fmtMS },
      { label: "loss", scale: "loss", stroke: c.loss, value: fmtPct, paths: () => null, points: { show: false } },
    ],
    hooks: { draw: [faultyBands(state), lossDots(state, "loss")] },
  }, [d.xs, d.avg, d.p99, d.loss], el);
  return { u, state, cols: dd => [dd.xs, dd.avg, dd.p99, dd.loss] };
}

// Jitter distribution per minute: band p10–p99, p50 line, dashed max.
function jitterChart(el, d, c) {
  const thin = hexToRgba(c.jitter, 0.5);
  const state = { d };
  const u = new uPlot({
    width: el.clientWidth, height: 160,
    cursor: { sync: { key: "jitter" } },
    scales: { x: xScale(), y: { range: yRange } },
    axes: axes(c),
    bands: [{ series: [2, 1], fill: hexToRgba(c.jitter, 0.16) }],
    series: [
      {},
      { label: "p10", stroke: thin, width: 1, value: fmtMS },
      { label: "p99", stroke: thin, width: 1, value: fmtMS },
      { label: "p50", stroke: c.jitter, width: 2, value: fmtMS },
      { label: "max", stroke: c.envelope, width: 1, dash: [4, 3], value: fmtMS },
    ],
    hooks: { draw: [faultyBands(state)] },
  }, [d.xs, d.jP10, d.jP99, d.jP50, d.jMax], el);
  return { u, state, cols: dd => [dd.xs, dd.jP10, dd.jP99, dd.jP50, dd.jMax] };
}

function rttChartLive(el, d, c) {
  const state = { d };
  const u = new uPlot({
    width: el.clientWidth, height: 190,
    cursor: { sync: { key: "jitter" } },
    scales: { x: xScale(), y: { range: yRange } },
    axes: axes(c),
    series: [
      {},
      { label: "RTT", stroke: c.rtt, width: 1.5, points: { show: true, size: 4 }, value: fmtMS },
    ],
    hooks: { draw: [lossDots(state, "lost")] },
  }, [d.xs, d.rtt], el);
  return { u, state, cols: dd => [dd.xs, dd.rtt] };
}

function jitterChartLive(el, d, c) {
  const state = { d };
  const u = new uPlot({
    width: el.clientWidth, height: 160,
    cursor: { sync: { key: "jitter" } },
    scales: { x: xScale(), y: { range: yRange } },
    axes: axes(c),
    series: [
      {},
      { label: "jitter", stroke: c.jitter, width: 1.5, points: { show: true, size: 4 }, value: fmtMS },
    ],
  }, [d.xs, d.jit], el);
  return { u, state, cols: dd => [dd.xs, dd.jit] };
}

// ---- Stats lines ----

function lastStats(rows) {
  const last = [...rows].reverse().find(r => !faulty(r));
  if (!last) return `latest: <b class="err">faulty</b> — too few samples to measure`;
  return `latest: <b>${last.avg_ms.toFixed(1)} ms</b> avg RTT · ` +
    `jitter p50 <b>${last.jitter_p50_ms.toFixed(2)}</b> (max <b>${last.jitter_max_ms.toFixed(2)}</b>) ms · ` +
    `<b>${last.loss_pct.toFixed(1)}%</b> loss`;
}

function liveStats(pts) {
  const oks = pts.filter(p => !p.lost);
  const lostPct = 100 * (pts.length - oks.length) / pts.length;
  if (!oks.length) return `live: <b class="err">100%</b> loss over ${pts.length} samples`;
  let sumj = 0, maxj = 0, n = 0, prev = null;
  for (const p of pts) {
    if (p.lost) continue;
    if (prev != null) { const dj = Math.abs(p.rtt_ms - prev); sumj += dj; maxj = Math.max(maxj, dj); n++; }
    prev = p.rtt_ms;
  }
  const last = oks[oks.length - 1];
  return `live: <b>${last.rtt_ms.toFixed(1)} ms</b> RTT · ` +
    `jitter <b>${(n ? sumj / n : 0).toFixed(2)}</b> (max <b>${maxj.toFixed(2)}</b>) ms · ` +
    `<b>${lostPct.toFixed(1)}%</b> loss over ${pts.length} samples`;
}

// ---- Load + render ----

// Fetch and prepare one target's render item for the given mode.
async function loadItem(ti, mode) {
  if (mode === "live") {
    const pts = await fetchJSON(`/api/live?target=${encodeURIComponent(ti.target)}`);
    pts.forEach(p => { p._t = Date.parse(p.t); });
    const win = pts.filter(p => p._t >= Date.now() - rangeMinutes * 60000);
    return { ti, has: win.length > 0, d: win.length ? toLiveColumns(win) : null, stats: win.length ? liveStats(win) : "" };
  }
  const from = new Date(Date.now() - rangeMinutes * 60000).toISOString();
  const rows = await fetchJSON(`/api/history?target=${encodeURIComponent(ti.target)}&from=${encodeURIComponent(from)}`);
  rows.forEach(r => { r._t = Date.parse(r.minute); });
  return { ti, has: rows.length > 0, d: rows.length ? toColumns(rows) : null, stats: rows.length ? lastStats(rows) : "" };
}

function cardHeads(mode) {
  return mode === "live"
    ? `<h3>RTT (ms) — live 250 ms samples</h3><div class="chart chart-rtt"></div>
       <h3>Jitter (ms) — instantaneous |Δrtt| between samples</h3><div class="chart chart-jit"></div>`
    : `<h3>RTT (ms) <span class="hint">avg · p99 · <i class="fault"></i> faulty minute</span></h3>
       <div class="chart chart-rtt"></div>
       <h3>Jitter (ms) — band p10–p99, line p50, dashed max</h3>
       <div class="chart chart-jit"></div>`;
}

function destroyCharts() {
  for (const card of view.cards.values()) { card.rtt.u.destroy(); card.jit.u.destroy(); }
  view.cards = new Map();
}

// Full rebuild: replace DOM and create fresh chart instances.
function buildCharts(main, mode, items, c) {
  destroyCharts();
  main.innerHTML = items.map(it => {
    const body = it.has
      ? `<div class="stats">${it.stats}</div>${cardHeads(mode)}`
      : `<p class="empty">No data in range.</p>`;
    return `<section class="card" data-target="${it.ti.target}">
      <h2>${it.ti.pop.toUpperCase()} — ${it.ti.target}</h2>${body}</section>`;
  }).join("");
  for (const it of items) {
    if (!it.has) continue;
    const el = main.querySelector(`[data-target="${CSS.escape(it.ti.target)}"]`);
    const rttEl = el.querySelector(".chart-rtt"), jitEl = el.querySelector(".chart-jit");
    const mk = mode === "live"
      ? { rtt: rttChartLive(rttEl, it.d, c), jit: jitterChartLive(jitEl, it.d, c) }
      : { rtt: rttChart(rttEl, it.d, c), jit: jitterChart(jitEl, it.d, c) };
    view.cards.set(it.ti.target, mk);
  }
}

// Incremental update: feed new data to existing charts, refresh stats text.
function updateCharts(items) {
  for (const it of items) {
    const card = view.cards.get(it.ti.target);
    if (!card || !it.has) continue;
    const statsEl = document.querySelector(`[data-target="${CSS.escape(it.ti.target)}"] .stats`);
    if (statsEl) statsEl.innerHTML = it.stats;
    for (const h of [card.rtt, card.jit]) {
      h.state.d = it.d;
      h.u.setData(h.cols(it.d));
    }
  }
}

async function refresh() {
  const main = document.getElementById("charts");
  const targets = await fetchJSON("/api/targets");
  if (!targets.length) {
    destroyCharts();
    view.sig = null;
    main.innerHTML = `<p class="empty">No data yet — the collector writes its first row after one full minute.</p>`;
    return;
  }
  const mode = rangeMinutes <= LIVE_MAX_MIN ? "live" : "history";
  const items = await Promise.all(targets.map(ti => loadItem(ti, mode)));
  const sig = mode + "::" + items.map(it => it.ti.target + (it.has ? "1" : "0")).join("|");
  if (sig !== view.sig) {
    buildCharts(main, mode, items, palette());
    view.sig = sig;
  } else {
    updateCharts(items);
  }
}

let resizeTimer;
window.addEventListener("resize", () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => {
    for (const card of view.cards.values()) {
      for (const h of [card.rtt, card.jit]) {
        h.u.setSize({ width: h.u.root.parentElement.clientWidth, height: h.u.height });
      }
    }
  }, 150);
});

// Live ranges refresh every few seconds; aggregate ranges every minute.
function scheduleRefresh() {
  if (refreshTimer) clearInterval(refreshTimer);
  const ms = rangeMinutes <= LIVE_MAX_MIN ? 2000 : 60000;
  refreshTimer = setInterval(() => refresh().catch(console.error), ms);
}

function selectRange(b) {
  document.querySelectorAll("#ranges button").forEach(x => x.classList.remove("active"));
  b.classList.add("active");
  rangeMinutes = Number(b.dataset.m);
  location.hash = b.textContent; // shareable/bookmarkable, e.g. #3m
}

document.getElementById("ranges").addEventListener("click", e => {
  const b = e.target.closest("button[data-m]");
  if (!b) return;
  selectRange(b);
  refresh().catch(console.error);
  scheduleRefresh();
});

// Honour a range in the URL hash (e.g. #3m) on load; default stays 24h.
const hash = decodeURIComponent(location.hash.replace(/^#/, ""));
const preset = [...document.querySelectorAll("#ranges button")].find(b => b.textContent === hash);
if (preset) selectRange(preset);

refresh().catch(console.error);
scheduleRefresh();
