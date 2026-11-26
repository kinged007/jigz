"use strict";

// ---------- constants ----------
// Every node type has its own colour and shape. Open issues and pull requests
// are filled, closed and merged ones are outlined.
const TYPES = {
  repo:      { color: "#ffffff", shape: "star",     size: 5,   min: 8,   name: "REPO" },
  stage:     { color: "#22d3ee", shape: "hexagon",  size: 3.2, min: 6,   name: "STAGE" },
  issue:     { color: "#4ade80", shape: "circle",   size: 1.5, min: 3,   name: "ISSUE" },
  pr:        { color: "#c084fc", shape: "diamond",  size: 1.7, min: 3.4, name: "PULL" },
  commit:    { color: "#fb923c", shape: "square",   size: 1.2, min: 2.6, name: "COMMIT" },
  file:      { color: "#a8b8cc", shape: "triangle", size: 1.1, min: 2.8, name: "FILE" },
  dir:       { color: "#60a5fa", shape: "box",      size: 2.0, min: 4,   name: "DIR", hollow: true },
  user:      { color: "#fde047", shape: "star",     size: 4,   min: 6.5, name: "USER" },
  label:     { color: "#f472b6", shape: "pentagon", size: 2.6, min: 4.5, name: "LABEL" },
  keyword:   { color: "#2dd4bf", shape: "plus",     size: 1.8, min: 3.6, name: "KEYWORD" },
  scope:     { color: "#a3e635", shape: "down",     size: 2.0, min: 3.8, name: "SCOPE" },
  milestone: { color: "#fb7185", shape: "circle",   size: 2.4, min: 5,   name: "MILESTONE", hollow: true },
};
const CORE = new Set(["issue", "pr", "commit"]);
const TABS = [
  ["ALL", null],
  ["ISSUES", ["issue", "label", "stage", "keyword", "scope", "user", "milestone"]],
  ["PULLS", ["pr", "label", "stage", "commit", "user", "issue"]],
  ["CODE", ["repo", "commit", "file", "dir", "scope", "pr"]],
  ["PEOPLE", ["user", "issue", "pr", "commit"]],
  ["TOPICS", ["keyword", "scope", "issue", "pr"]],
  ["WORKFLOW", ["stage", "issue", "pr"]],
];
const LINK_GROUPS = {
  contains: "code", touches: "code", merged: "code", refs: "code",
  stage: "flow", flow: "flow",
  authored: "social", committed: "social", engaged: "social", assigned: "social",
  label: "meaning", topic: "meaning", scope: "meaning", milestone: "meaning",
  closes: "xref", mentions: "xref", duplicate: "xref",
};
const GROUP_STYLE = {
  code:    ["rgba(96,165,250,", 0.13],
  flow:    ["rgba(34,211,238,", 0.05],
  social:  ["rgba(253,224,71,", 0.035],
  meaning: ["rgba(244,114,182,", 0.05],
  xref:    ["rgba(220,245,255,", 0.35],
};
const OPS = { labeled: "+", unlabeled: "−", closed: "×", reopened: "↺", merged: "⇢", xref: "↔", connected: "↔", assigned: "@", review: "✓", duplicate: "≡", comment: "»", commit: "◆" };

// ---------- state ----------
const S = {
  g: null, nodes: [], links: [], adj: [], byId: new Map(), vis: null, hidden: new Set(),
  yaw: 0.6, pitch: -0.32, dist: 1100, target: [0, 0, 0], goal: null, goalDist: 0,
  sel: -1, hover: -1, tab: 0, query: "", matches: null, neigh: new Set(),
  timeFrom: -Infinity, timeCut: Infinity, playing: false, lastInput: 0, drag: false,
  winDirty: true, panelDirty: true, panelAt: 0, camT: 0, stDirty: true, gl: null,
  proj: null, started: performance.now(), frames: 0, fps: 0, fpsT: performance.now(),
};
const $ = (id) => document.getElementById(id);
const fmt = (n) => n.toLocaleString("en-US");
const esc = (s) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const day = (t) => t ? new Date(t * 1000).toISOString().slice(0, 10) : "—";
const dur = (h) => h < 1 ? Math.round(h * 60) + "m" : h < 48 ? h.toFixed(1) + "h" : (h / 24).toFixed(1) + "d";
const ago = (iso) => { if (!iso) return "never"; const m = (Date.now() - Date.parse(iso)) / 60000; return m < 60 ? Math.round(m) + "m ago" : m < 2880 ? Math.round(m / 60) + "h ago" : Math.round(m / 1440) + "d ago"; };
const median = (a) => { if (!a.length) return 0; const s = [...a].sort((x, y) => x - y), m = s.length >> 1; return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2; };

// ---------- load ----------
const getJSON = (url) => fetch(url, { cache: "no-store" }).then((r) => { if (!r.ok) throw new Error(url + " " + r.status); return r.json(); });

// /status tells the page whether the server refreshes by itself. Without
// -refresh the page loads once and shows no live indicator.
async function start() {
  try {
    const st = await getJSON("status").catch(() => ({ refresh: 0, version: "" }));
    const g = await getJSON("graph.json").catch((e) => { throw new Error(e.message + " — run: explorer build"); });
    init(g);
    if (st.refresh > 0) startLive(st);
  } catch (e) { $("sub").textContent = String(e); console.error(e); }
}
start();

// Builds everything derived from the graph. Nodes that exist in prev keep
// their position, so a live refresh does not move what is on screen.
function index(g, prev) {
  S.g = g;
  S.nodes = g.nodes;
  S.links = g.links;
  S.adj = S.nodes.map(() => []);
  S.byId = new Map();
  S.nodes.forEach((n, i) => { S.byId.set(n.id, i); n.i = i; });
  for (const l of S.links) { S.adj[l.s].push(l); S.adj[l.t].push(l); l.g = LINK_GROUPS[l.k] || "meaning"; }
  S.contains = S.links.filter((l) => l.k === "contains");
  S.kwNodes = S.nodes.filter((n) => n.type === "keyword");
  S.stageId = new Map(g.workflow.stages.map((s) => [s.name, s.id]));
  S.stageOrder = new Map(g.workflow.stages.map((s) => [s.name, s.order]));
  S.vis = new Uint8Array(S.nodes.length);

  if (!prev) {
    // Normalise the layout so the 95th percentile radius is 320 units.
    const rs = S.nodes.map((n) => Math.hypot(...n.p)).sort((a, b) => a - b);
    S.k = 320 / (rs[Math.floor(rs.length * 0.95)] || 1);
  }
  const fresh = [];
  for (const n of S.nodes) {
    const o = prev?.get(n.id);
    if (o) { n.x = o.x; n.y = o.y; n.z = o.z; }
    else if (prev) fresh.push(n);
    else { n.x = n.p[0] * S.k; n.y = n.p[1] * S.k; n.z = n.p[2] * S.k; }
    n.r = (TYPES[n.type]?.size || 1.5) * (1 + Math.sqrt(n.deg) * 0.12);
    n.hid = S.hidden.has(n.type);
    n.sig = signature(n);
  }
  placeNew(fresh);
  S.labelled = S.nodes.filter((n) => ["stage", "user", "repo"].includes(n.type) || (n.deg > 40 && ["label", "scope", "keyword", "dir"].includes(n.type)))
    .sort((a, b) => b.deg - a.deg).slice(0, 70).map((n) => n.i);
  S.proj = { x: new Float32Array(S.nodes.length), y: new Float32Array(S.nodes.length), s: new Float32Array(S.nodes.length), z: new Float32Array(S.nodes.length), v: new Uint8Array(S.nodes.length) };

  // Events of all items and commits, oldest first, for the event log.
  S.events = [];
  for (const n of S.nodes) {
    if (n.meta?.events) for (const e of n.meta.events) if (e[1] !== "comment") S.events.push({ t: e[0], type: e[1], node: n.i, x: e[3] });
    if (n.type === "commit") S.events.push({ t: n.t, type: "commit", node: n.i });
  }
  S.events.sort((a, b) => a.t - b.t);
  S.gl?.load(S);
  S.stDirty = true;
}

const signature = (n) => `${n.state}|${n.title}|${(n.meta?.labels || []).join(",")}`;

// A new node starts at the mean position of the nodes it links to, which is
// where the layout would roughly put it. Nodes whose neighbours are all new
// are placed in later passes.
function placeNew(fresh) {
  let todo = fresh;
  for (let pass = 0; pass < 4 && todo.length; pass++) {
    const left = [];
    for (const n of todo) {
      let sx = 0, sy = 0, sz = 0, c = 0;
      for (const l of S.adj[n.i]) {
        const m = S.nodes[l.s === n.i ? l.t : l.s];
        if (m.x !== undefined) { sx += m.x; sy += m.y; sz += m.z; c++; }
      }
      if (c) { n.x = sx / c + (Math.random() - 0.5) * 12; n.y = sy / c + (Math.random() - 0.5) * 12; n.z = sz / c + (Math.random() - 0.5) * 12; }
      else left.push(n);
    }
    todo = left;
  }
  for (const n of todo) { n.x = n.p[0] * S.k; n.y = n.p[1] * S.k; n.z = n.p[2] * S.k; }
}

function renderHeader() {
  const g = S.g, c = g.stats.counts, r = g.repo;
  $("sub").textContent = `SRC github / ${r.nameWithOwner} / ${fmt(c.issue || 0)} issues / ${fmt(c.pr || 0)} pulls / ${fmt(c.commit || 0)} commits / ${fmt(c.file || 0)} files`;
  $("cmdrepo").textContent = r.nameWithOwner;
  $("hudCount").textContent = `GRAPH / ${fmt(S.nodes.length)} NODES · ${fmt(S.links.length)} LINKS`;
  renderLive();
}

function init(g) {
  S.gl = new URLSearchParams(location.search).get("renderer") === "2d" ? null : createGL(glCanvas);
  glCanvas.hidden = !S.gl;
  glCanvas.addEventListener("webglcontextlost", (e) => { e.preventDefault(); S.gl = null; glCanvas.hidden = true; });
  index(g, null);
  buildLegend();
  $("tabs").innerHTML = TABS.map(([n], i) => `<button data-i="${i}" class="${i === 0 ? "on" : ""}">${n}</button>`).join("");
  $("tabs").onclick = (e) => { const b = e.target.closest("button"); if (b) setTab(+b.dataset.i); };

  renderHeader();
  setupInput();
  setupScrubber();
  computeVisibility();
  refreshPanels();
  applyHash();
  requestAnimationFrame(frame);
}

// ---------- live refresh ----------
// The server rewrites graph.json every -refresh interval. The page polls
// /status and, when the file changed, merges the new graph into the running
// one: the camera, selection, search, tab, hidden types and time window stay
// as they are, and only the panels whose content changed are redrawn.
const LIVE_NAMES = { issue: "issues", pr: "pulls", commit: "commits" };
const LIVE = { on: false, version: "", nextAt: 0, syncing: false, error: "", offline: false, busy: false };

function startLive(st) {
  LIVE.on = true;
  LIVE.version = st.version;
  readStatus(st);
  renderLive();
  setInterval(pollStatus, 3000);
}

function readStatus(st) {
  LIVE.syncing = st.syncing; LIVE.error = st.error || ""; LIVE.nextAt = Date.now() + st.nextIn * 1000; LIVE.offline = false;
}

async function pollStatus() {
  if (LIVE.busy) return;
  LIVE.busy = true;
  try {
    const st = await getJSON("status");
    readStatus(st);
    if (st.version !== LIVE.version) {
      applyGraph(await getJSON("graph.json"));
      LIVE.version = st.version;
    }
  } catch (e) { LIVE.offline = true; console.error(e); }
  LIVE.busy = false;
  renderLive();
}

function renderLive() {
  const live = $("liveind");
  live.hidden = !LIVE.on;
  $("synced").textContent = "SYNCED " + ago(S.g.syncedAt).toUpperCase();
  if (!LIVE.on) return;
  const left = Math.max(0, Math.round((LIVE.nextAt - Date.now()) / 1000));
  const state = LIVE.offline ? "OFFLINE" : LIVE.syncing ? "SYNCING…" : "NEXT " + String(Math.floor(left / 60)).padStart(2, "0") + ":" + String(left % 60).padStart(2, "0");
  $("countdown").textContent = state;
  $("liveind").classList.toggle("off", LIVE.offline);
  $("liveind").title = LIVE.error ? "last refresh failed: " + LIVE.error : "";
  $("liveind").classList.toggle("err", !!LIVE.error);
}

function applyGraph(g) {
  const prev = new Map(S.nodes.map((n) => [n.id, n]));
  const selId = S.sel >= 0 ? S.nodes[S.sel].id : null;
  index(g, prev);

  const added = {}, changed = {};
  for (const n of S.nodes) {
    const o = prev.get(n.id);
    if (!o) added[n.type] = (added[n.type] || 0) + 1;
    else if (o.sig !== n.sig && CORE.has(n.type)) changed[n.type] = (changed[n.type] || 0) + 1;
  }

  S.sel = selId && S.byId.has(selId) ? S.byId.get(selId) : -1;
  S.hover = -1;
  S.stDirty = true;
  S.neigh = new Set();
  if (S.sel >= 0) for (const l of S.adj[S.sel]) S.neigh.add(l.s === S.sel ? l.t : l.s);
  rescale();
  S.liveApply = true;
  computeVisibility();
  refreshPanels();
  S.liveApply = false;
  if (S.query) { S.query = ""; search($("q").value); }
  renderHeader();
  drawSpark();

  const list = (o) => ["issue", "pr", "commit"].filter((t) => o[t]).map((t) => `${o[t]} ${LIVE_NAMES[t]}`).join(", ");
  const msg = [list(added) && "new: " + list(added), list(changed) && "updated: " + list(changed)].filter(Boolean).join(" · ");
  status("live :: " + (msg || "refreshed, no changes"));
}

// ---------- shapes ----------
function addShape(p, shape, x, y, r) {
  switch (shape) {
    case "circle": p.moveTo(x + r, y); p.arc(x, y, r, 0, Math.PI * 2); break;
    case "diamond": p.moveTo(x, y - r * 1.3); p.lineTo(x + r * 1.05, y); p.lineTo(x, y + r * 1.3); p.lineTo(x - r * 1.05, y); p.closePath(); break;
    case "square": p.rect(x - r * 0.85, y - r * 0.85, r * 1.7, r * 1.7); break;
    case "box": p.rect(x - r * 0.9, y - r * 0.9, r * 1.8, r * 1.8); break;
    case "triangle": p.moveTo(x, y - r * 1.1); p.lineTo(x + r * 1.05, y + r * 0.85); p.lineTo(x - r * 1.05, y + r * 0.85); p.closePath(); break;
    case "down": p.moveTo(x, y + r * 1.1); p.lineTo(x + r * 1.05, y - r * 0.85); p.lineTo(x - r * 1.05, y - r * 0.85); p.closePath(); break;
    case "plus": p.rect(x - r, y - r * 0.32, r * 2, r * 0.64); p.rect(x - r * 0.32, y - r, r * 0.64, r * 2); break;
    case "hexagon": poly(p, x, y, r * 1.1, 6, 0); break;
    case "pentagon": poly(p, x, y, r * 1.1, 5, -Math.PI / 2); break;
    case "star": star(p, x, y, r * 1.2); break;
  }
}
function poly(p, x, y, r, n, a0) {
  for (let k = 0; k < n; k++) { const a = a0 + (k * 2 * Math.PI) / n; k ? p.lineTo(x + Math.cos(a) * r, y + Math.sin(a) * r) : p.moveTo(x + Math.cos(a) * r, y + Math.sin(a) * r); }
  p.closePath();
}
function star(p, x, y, r) {
  for (let k = 0; k < 10; k++) { const a = -Math.PI / 2 + (k * Math.PI) / 5, rr = k % 2 ? r * 0.45 : r; k ? p.lineTo(x + Math.cos(a) * rr, y + Math.sin(a) * rr) : p.moveTo(x + Math.cos(a) * rr, y + Math.sin(a) * rr); }
  p.closePath();
}
function svgShape(type, filled) {
  const t = TYPES[type], c = t.color, s = 14, h = s / 2, r = 5;
  const pts = (n, a0, rr) => Array.from({ length: n }, (_, k) => { const a = a0 + (k * 2 * Math.PI) / n; return `${(h + Math.cos(a) * rr).toFixed(1)},${(h + Math.sin(a) * rr).toFixed(1)}`; }).join(" ");
  const fill = filled && !t.hollow ? `fill="${c}"` : `fill="none" stroke="${c}" stroke-width="1.4"`;
  let body;
  switch (t.shape) {
    case "circle": body = `<circle cx="${h}" cy="${h}" r="${r}" ${fill}/>`; break;
    case "diamond": body = `<polygon points="${h},${h - 6.5} ${h + 5.2},${h} ${h},${h + 6.5} ${h - 5.2},${h}" ${fill}/>`; break;
    case "square": case "box": body = `<rect x="${h - 4.5}" y="${h - 4.5}" width="9" height="9" ${fill}/>`; break;
    case "triangle": body = `<polygon points="${h},${h - 5.5} ${h + 5.2},${h + 4.2} ${h - 5.2},${h + 4.2}" ${fill}/>`; break;
    case "down": body = `<polygon points="${h},${h + 5.5} ${h + 5.2},${h - 4.2} ${h - 5.2},${h - 4.2}" ${fill}/>`; break;
    case "plus": body = `<path d="M${h - 5} ${h - 1.6}h10v3.2h-10zM${h - 1.6} ${h - 5}h3.2v10h-3.2z" fill="${c}"/>`; break;
    case "hexagon": body = `<polygon points="${pts(6, 0, 5.8)}" ${fill}/>`; break;
    case "pentagon": body = `<polygon points="${pts(5, -Math.PI / 2, 5.8)}" ${fill}/>`; break;
    case "star": body = `<polygon points="${Array.from({ length: 10 }, (_, k) => { const a = -Math.PI / 2 + (k * Math.PI) / 5, rr = k % 2 ? 3 : 6.5; return `${(h + Math.cos(a) * rr).toFixed(1)},${(h + Math.sin(a) * rr).toFixed(1)}`; }).join(" ")}" ${fill}/>`; break;
  }
  return `<svg width="${s}" height="${s}" viewBox="0 0 ${s} ${s}">${body}</svg>`;
}
function buildLegend() {
  const order = ["issue", "pr", "commit", "file", "dir", "user", "label", "stage", "keyword", "scope", "milestone", "repo"];
  $("legend").innerHTML = order.map((t) => `<span class="chip" data-type="${t}" title="click to show or hide">${svgShape(t, true)}${TYPES[t].name} <b id="lg-${t}"></b></span>`).join("")
    + `<span class="note">filled = open · outlined = closed / merged</span>`;
  $("legend").onclick = (e) => {
    const c = e.target.closest(".chip");
    if (!c) return;
    const t = c.dataset.type;
    S.hidden.has(t) ? S.hidden.delete(t) : S.hidden.add(t);
    c.classList.toggle("off", S.hidden.has(t));
    for (const n of S.nodes) n.hid = S.hidden.has(n.type);
    S.stDirty = true;
  };
}

// ---------- time window ----------
// Items and commits are inside the window when created within it. Everything
// else (labels, people, files, ...) is shown when it is linked to one of them.
function computeVisibility() {
  const from = S.timeFrom, to = S.timeCut, vis = S.vis, timed = from > -Infinity || to < Infinity;
  for (let i = 0; i < S.nodes.length; i++) {
    const n = S.nodes[i];
    vis[i] = CORE.has(n.type) ? (n.t >= from && n.t <= to ? 1 : 0) : (timed ? 0 : 1);
  }
  if (timed) {
    for (const l of S.links) {
      const a = S.nodes[l.s], b = S.nodes[l.t];
      if (CORE.has(a.type) && !CORE.has(b.type) && vis[l.s]) vis[l.t] = 1;
      else if (CORE.has(b.type) && !CORE.has(a.type) && vis[l.t]) vis[l.s] = 1;
    }
    for (let pass = 0, changed = true; changed && pass < 16; pass++) {
      changed = false;
      for (const l of S.contains) if (vis[l.t] && !vis[l.s]) { vis[l.s] = 1; changed = true; }
    }
  }
  vis[S.byId.get("repo")] = 1;
  S.winDirty = false;
  S.stDirty = true;
}

function windowStats() {
  const items = [], commits = [];
  for (const n of S.nodes) {
    if (!S.vis[n.i]) continue;
    if (n.type === "issue" || n.type === "pr") items.push(n); else if (n.type === "commit") commits.push(n);
  }
  return { items, commits, from: S.timeFrom, to: S.timeCut };
}

function rangeLabel() {
  return `${S.timeFrom === -Infinity ? "START" : day(S.timeFrom)} → ${S.timeCut === Infinity ? "NOW" : day(S.timeCut)}`;
}

// ---------- camera / projection ----------
function project(w, h) {
  const P = S.proj, f = Math.min(w, h) * 1.15;
  const cy = Math.cos(S.yaw), sy = Math.sin(S.yaw), cp = Math.cos(S.pitch), sp = Math.sin(S.pitch);
  const [tx, ty, tz] = S.target;
  for (let i = 0; i < S.nodes.length; i++) {
    const n = S.nodes[i];
    if (!S.vis[i] || n.hid) { P.v[i] = 0; continue; }
    const x = n.x - tx, y = n.y - ty, z = n.z - tz;
    const x1 = x * cy - z * sy, z1 = x * sy + z * cy;
    const y2 = y * cp - z1 * sp, z2 = y * sp + z1 * cp;
    const d = z2 + S.dist;
    if (d < 20) { P.v[i] = 0; continue; }
    const s = f / d;
    P.x[i] = w / 2 + x1 * s; P.y[i] = h / 2 + y2 * s; P.s[i] = s; P.z[i] = d; P.v[i] = 1;
  }
}

function panBy(start, dx, dy) {
  const w = canvas.clientWidth, h = canvas.clientHeight, k = S.dist / (Math.min(w, h) * 1.15);
  const cy = Math.cos(S.yaw), sy = Math.sin(S.yaw), cp = Math.cos(S.pitch), sp = Math.sin(S.pitch);
  const right = [cy, 0, -sy], up = [-sp * sy, cp, -sp * cy];
  for (let a = 0; a < 3; a++) S.target[a] = start[a] - (right[a] * dx + up[a] * dy) * k;
  S.goal = null; S.goalDist = 0;
}

function resetView() { S.target = [0, 0, 0]; S.goal = null; S.goalDist = 0; S.dist = 1100; }

// ---------- rendering ----------
const canvas = $("graph"), ctx = canvas.getContext("2d"), glCanvas = $("gl");
const dprNow = () => Math.min(2, window.devicePixelRatio || 1);
const sprites = new Map();
function sprite(color) {
  if (sprites.has(color)) return sprites.get(color);
  const c = document.createElement("canvas"); c.width = c.height = 64;
  const x = c.getContext("2d"), gr = x.createRadialGradient(32, 32, 0, 32, 32, 32);
  gr.addColorStop(0, color + "aa"); gr.addColorStop(0.3, color + "44"); gr.addColorStop(1, color + "00");
  x.fillStyle = gr; x.fillRect(0, 0, 64, 64);
  sprites.set(color, c);
  return c;
}

function frame(now) {
  const dpr = dprNow(), W = canvas.clientWidth, H = canvas.clientHeight;
  if (canvas.width !== Math.round(W * dpr) || canvas.height !== Math.round(H * dpr)) { canvas.width = Math.round(W * dpr); canvas.height = Math.round(H * dpr); }
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

  if (!S.drag && now - S.lastInput > 2500) S.yaw += 0.0009;
  if (S.goal) {
    for (let k = 0; k < 3; k++) S.target[k] += (S.goal[k] - S.target[k]) * 0.08;
    if (S.goalDist) S.dist += (S.goalDist - S.dist) * 0.06;
  }
  if (S.playing) stepPlay(now);
  if (S.winDirty) computeVisibility();
  if (S.panelDirty && now - S.panelAt > (S.playing || S.scrubbing ? 220 : 0)) { S.panelAt = now; S.panelDirty = false; refreshPanels(); }

  project(W, H);
  if (S.gl) {
    if (S.stDirty) { S.gl.state(S); S.stDirty = false; }
    S.gl.draw(S, W, H, dpr);
    ctx.clearRect(0, 0, W, H);
  } else {
    ctx.fillStyle = "#04070b"; ctx.fillRect(0, 0, W, H);
    drawLinks();
    drawNodes();
  }
  drawLabels(W, H);
  drawSelection(W, H);
  drawRadar(now);

  S.frames++;
  if (now - S.fpsT > 1000) { S.fps = Math.round(S.frames * 1000 / (now - S.fpsT)); S.frames = 0; S.fpsT = now; $("fps").textContent = S.fps + "FPS " + (S.gl ? "GL" : "2D"); tickClock(); renderLive(); }
  if (now - S.camT > 100) { S.camT = now; $("hudCam").textContent = `AZ ${(((S.yaw * 180 / Math.PI) % 360 + 360) % 360).toFixed(1)}° Z ${Math.round(S.dist / 4)}`; }
  requestAnimationFrame(frame);
}

function inTab(n) {
  const types = TABS[S.tab][1];
  return !types || types.includes(n.type);
}

function nodeAlpha(i) {
  const n = S.nodes[i];
  let a = Math.max(0.35, Math.min(1, 1000 / S.proj.z[i]));
  if (!inTab(n)) a *= 0.1;
  if (S.matches && !S.matches.has(i)) a *= 0.15;
  if (S.sel >= 0 && i !== S.sel && !S.neigh.has(i)) a *= 0.4;
  return a;
}

function radiusPx(i) {
  const n = S.nodes[i], t = TYPES[n.type];
  return Math.min(16, Math.max(t.min, n.r * S.proj.s[i] * 1.15));
}

function drawLinks() {
  const P = S.proj, sel = S.sel;
  const paths = {};
  for (const g in GROUP_STYLE) paths[g] = new Path2D();
  const dimAll = S.matches || sel >= 0;
  for (const l of S.links) {
    if (!P.v[l.s] || !P.v[l.t]) continue;
    const a = S.nodes[l.s], b = S.nodes[l.t];
    if (!inTab(a) || !inTab(b)) continue;
    if (S.matches && !(S.matches.has(l.s) || S.matches.has(l.t))) continue;
    const p = paths[l.g];
    p.moveTo(P.x[l.s], P.y[l.s]); p.lineTo(P.x[l.t], P.y[l.t]);
  }
  ctx.lineWidth = 0.6;
  for (const g in paths) {
    const [c, a] = GROUP_STYLE[g];
    ctx.strokeStyle = c + (dimAll ? a * 0.45 : a) + ")";
    ctx.stroke(paths[g]);
  }
  if (sel >= 0) {
    const p = new Path2D(), x = new Path2D();
    for (const l of S.adj[sel]) {
      if (!P.v[l.s] || !P.v[l.t]) continue;
      const q = l.g === "xref" || l.g === "flow" ? x : p;
      q.moveTo(P.x[l.s], P.y[l.s]); q.lineTo(P.x[l.t], P.y[l.t]);
    }
    ctx.lineWidth = 0.9; ctx.strokeStyle = "rgba(127,216,255,0.55)"; ctx.stroke(p);
    ctx.lineWidth = 1.4; ctx.strokeStyle = "rgba(235,252,255,0.9)"; ctx.stroke(x);
  }
}

function isOutlined(n) {
  const t = TYPES[n.type];
  if (t.hollow) return true;
  return (n.type === "issue" || n.type === "pr") && n.state !== "OPEN";
}

function drawNodes() {
  const P = S.proj, batches = new Map(), hot = [];
  const order = [];
  for (let i = 0; i < S.nodes.length; i++) if (P.v[i]) order.push(i);
  order.sort((a, b) => P.z[b] - P.z[a]);

  ctx.globalCompositeOperation = "lighter";
  for (const i of order) {
    const n = S.nodes[i], t = TYPES[n.type];
    if (n.deg > 25 && nodeAlpha(i) > 0.3) {
      const g = Math.min(34, radiusPx(i) * 3.2);
      ctx.globalAlpha = 0.55;
      ctx.drawImage(sprite(t.color), P.x[i] - g, P.y[i] - g, g * 2, g * 2);
    }
  }
  ctx.globalCompositeOperation = "source-over";

  for (const i of order) {
    const n = S.nodes[i], a = nodeAlpha(i);
    if (a < 0.03) continue;
    if (i === S.hover || i === S.sel || (S.matches && S.matches.has(i)) || (S.sel >= 0 && S.neigh.has(i))) { hot.push(i); continue; }
    const out = isOutlined(n), bucket = Math.min(5, Math.round(a * 5));
    const key = n.type + (out ? "o" : "f") + bucket;
    let b = batches.get(key);
    if (!b) { b = { path: new Path2D(), type: n.type, out, a: bucket / 5 }; batches.set(key, b); }
    addShape(b.path, TYPES[n.type].shape, P.x[i], P.y[i], radiusPx(i));
  }
  for (const b of batches.values()) {
    const c = TYPES[b.type].color;
    ctx.globalAlpha = Math.max(0.12, b.a) * (b.out ? 0.95 : 0.9);
    if (b.out) { ctx.strokeStyle = c; ctx.lineWidth = 1.3; ctx.stroke(b.path); } else { ctx.fillStyle = c; ctx.fill(b.path); }
  }
  for (const i of hot) {
    const n = S.nodes[i], t = TYPES[n.type], r = radiusPx(i) * (i === S.hover || i === S.sel ? 1.7 : 1.35);
    const p = new Path2D();
    addShape(p, t.shape, P.x[i], P.y[i], r);
    ctx.globalAlpha = 1;
    if (isOutlined(n)) { ctx.strokeStyle = t.color; ctx.lineWidth = 1.8; ctx.stroke(p); } else { ctx.fillStyle = t.color; ctx.fill(p); }
    ctx.strokeStyle = "rgba(255,255,255,0.9)"; ctx.lineWidth = 1; ctx.stroke(p);
  }
  ctx.globalAlpha = 1;
}

function drawLabels(W, H) {
  const P = S.proj;
  ctx.font = S.font || (S.font = "10px " + getComputedStyle(document.body).fontFamily);
  ctx.textBaseline = "middle";
  let list = S.labelled;
  if (S.sel >= 0) list = [...S.neigh].sort((a, b) => S.nodes[b].deg - S.nodes[a].deg).slice(0, 36);
  else if (S.matches) list = [...S.matches].slice(0, 40);
  for (const i of list) {
    if (!P.v[i] || i === S.sel) continue;
    const n = S.nodes[i];
    if (!inTab(n) && S.sel < 0) continue;
    const x = P.x[i], y = P.y[i];
    if (x < 0 || y < 0 || x > W || y > H) continue;
    ctx.fillStyle = n.type === "stage" ? "rgba(225,245,255,0.9)" : "rgba(170,195,215,0.75)";
    ctx.fillText(shortLabel(n), x + radiusPx(i) + 3, y - 6);
  }
}

function shortLabel(n) {
  if (n.type === "issue" || n.type === "pr") return n.label + (S.sel >= 0 ? " " + (n.title || "").slice(0, 34) : "");
  return n.label.length > 28 ? n.label.slice(0, 27) + "…" : n.label;
}

function drawSelection(W, H) {
  const callout = $("callout"), card = $("card");
  if (S.sel < 0 || !S.proj.v[S.sel]) { callout.hidden = card.hidden = true; return; }
  const P = S.proj, i = S.sel, x = P.x[i], y = P.y[i], t = TYPES[S.nodes[i].type];
  ctx.strokeStyle = "rgba(200,240,255,0.5)"; ctx.lineWidth = 1;
  ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(W, y); ctx.stroke();
  ctx.strokeStyle = t.color + "66";
  ctx.beginPath(); ctx.arc(x, y, Math.min(W, H) * 0.24, 0, Math.PI * 2); ctx.stroke();
  callout.hidden = card.hidden = false;
  callout.style.borderColor = t.color;
  const cx = Math.max(8, Math.min(W - callout.offsetWidth - 10, x + 30)), cy = Math.max(8, y - 44);
  callout.style.transform = `translate(${cx}px,${cy}px)`;
  const kx = Math.max(10, Math.min(W - card.offsetWidth - 10, x - card.offsetWidth - 20)), ky = Math.max(8, Math.min(H - card.offsetHeight - 30, y + 36));
  card.style.transform = `translate(${kx}px,${ky}px)`;
}

// ---------- radar (panel 04) ----------
const radar = $("radar"), rctx = radar.getContext("2d");
function drawRadar(now) {
  const dpr = window.devicePixelRatio || 1, R = 130;
  if (radar.width !== R * dpr) { radar.width = radar.height = R * dpr; }
  rctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  rctx.clearRect(0, 0, R, R);
  const c = R / 2;
  rctx.strokeStyle = "#1c2d3a"; rctx.lineWidth = 1;
  for (const r of [20, 40, 60]) { rctx.beginPath(); rctx.arc(c, c, r, 0, Math.PI * 2); rctx.stroke(); }
  rctx.beginPath(); rctx.moveTo(c - 62, c); rctx.lineTo(c + 62, c); rctx.moveTo(c, c - 62); rctx.lineTo(c, c + 62); rctx.stroke();
  const a = (now / 1400) % (Math.PI * 2);
  for (let k = 0; k < 14; k++) {
    rctx.strokeStyle = `rgba(127,216,255,${0.5 - k * 0.035})`;
    rctx.beginPath(); rctx.moveTo(c, c); rctx.lineTo(c + Math.cos(a - k * 0.03) * 60, c + Math.sin(a - k * 0.03) * 60); rctx.stroke();
  }
  for (const b of S.blips || []) {
    const age = ((a - b.a) % (Math.PI * 2) + Math.PI * 2) % (Math.PI * 2);
    rctx.globalAlpha = Math.max(0.3, 1 - age / 4);
    rctx.fillStyle = b.c || "#bdf0ff";
    rctx.fillRect(Math.round(c + Math.cos(b.a) * b.r) - 2, Math.round(c + Math.sin(b.a) * b.r) - 2, 4, 4);
  }
  rctx.globalAlpha = 1;
}

// ---------- input ----------
function setupInput() {
  const stage = $("stage");
  let down = null, moved = false;
  stage.addEventListener("contextmenu", (e) => e.preventDefault());
  stage.addEventListener("pointerdown", (e) => {
    if (e.button === 1) e.preventDefault();
    const pan = e.button === 1 || e.ctrlKey || e.metaKey || e.shiftKey;
    down = { x: e.clientX, y: e.clientY, yaw: S.yaw, pitch: S.pitch, target: [...S.target], pan };
    moved = false; S.drag = true;
    stage.classList.add(pan ? "pan" : "drag");
    stage.setPointerCapture(e.pointerId);
  });
  stage.addEventListener("pointermove", (e) => {
    S.lastInput = performance.now();
    const rect = canvas.getBoundingClientRect(), mx = e.clientX - rect.left, my = e.clientY - rect.top;
    if (down) {
      const dx = e.clientX - down.x, dy = e.clientY - down.y;
      if (Math.abs(dx) + Math.abs(dy) > 3) moved = true;
      if (down.pan) panBy(down.target, dx, dy);
      else { S.yaw = down.yaw + dx * 0.005; S.pitch = Math.max(-1.5, Math.min(1.5, down.pitch + dy * 0.005)); }
      return;
    }
    S.hover = pick(mx, my);
    const tip = $("tip");
    if (S.hover >= 0) {
      const n = S.nodes[S.hover];
      tip.hidden = false;
      tip.textContent = `${TYPES[n.type]?.name || n.type} ${n.label}${n.title && n.title !== n.label ? " · " + n.title : ""}`;
      tip.style.transform = `translate(${Math.max(4, Math.min(mx + 14, rect.width - tip.offsetWidth - 6))}px,${my + 14}px)`;
    } else tip.hidden = true;
  });
  stage.addEventListener("pointerup", (e) => {
    stage.classList.remove("drag", "pan"); S.drag = false;
    if (down && !moved && !down.pan) {
      const rect = canvas.getBoundingClientRect();
      select(pick(e.clientX - rect.left, e.clientY - rect.top), false);
    }
    down = null;
  });
  stage.addEventListener("wheel", (e) => { e.preventDefault(); S.lastInput = performance.now(); S.goalDist = 0; S.dist = Math.max(60, Math.min(4000, S.dist * Math.exp(e.deltaY * 0.001))); }, { passive: false });
  stage.addEventListener("pointerleave", () => { S.hover = -1; $("tip").hidden = true; });

  const q = $("q");
  q.addEventListener("input", () => search(q.value));
  q.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && S.matches && S.matches.size) {
      const vis = [...S.matches].filter((i) => S.vis[i]);
      const best = (vis.length ? vis : [...S.matches]).sort((a, b) => scoreMatch(b) - scoreMatch(a))[0];
      select(best, true);
    }
    if (e.key === "Escape") { q.value = ""; search(""); select(-1); }
  });
  window.addEventListener("keydown", (e) => {
    if (document.activeElement === q) return;
    if (e.key === "/") { e.preventDefault(); q.focus(); }
    if (e.key === "Escape") select(-1);
    if (e.key === "r") resetView();
  });
}

function pick(mx, my) {
  const P = S.proj;
  let best = -1, bd = 1e9;
  for (let i = 0; i < S.nodes.length; i++) {
    if (!P.v[i] || nodeAlpha(i) < 0.05) continue;
    const dx = P.x[i] - mx, dy = P.y[i] - my, d = dx * dx + dy * dy;
    const r = Math.max(7, radiusPx(i) + 3);
    if (d < r * r && d + P.z[i] * 1e-4 < bd) { bd = d + P.z[i] * 1e-4; best = i; }
  }
  return best;
}

function scoreMatch(i) {
  const n = S.nodes[i], q = S.query, l = n.label.toLowerCase();
  let s = n.deg * 0.01;
  if (l === q || l === "#" + q || l === "@" + q) s += 100;
  if (l.startsWith(q)) s += 10;
  return s;
}

function search(text) {
  S.stDirty = true;
  S.query = text.trim().toLowerCase();
  if (!S.query) { S.matches = null; $("qhint").textContent = "JIGZ"; status(""); return; }
  const q = S.query, m = new Set();
  const num = q.match(/^#(\d+)$/);
  for (const n of S.nodes) {
    if (num) { if (n.label === "#" + num[1]) m.add(n.i); continue; }
    if (n.label.toLowerCase().includes(q) || (n.title && n.title.toLowerCase().includes(q)) || n.id.toLowerCase().includes(q)) m.add(n.i);
  }
  S.matches = m;
  $("qhint").textContent = `${fmt(m.size)} MATCHES`;
  status(`query :: "${text.trim()}" · ${fmt(m.size)} nodes matched`);
}

function setTab(i) {
  S.tab = i;
  S.stDirty = true;
  [...$("tabs").children].forEach((b, k) => b.classList.toggle("on", k === i));
  status(`view :: ${TABS[i][0].toLowerCase()} · ${TABS[i][1] ? TABS[i][1].join(", ") : "every node type"}`);
  writeHash();
}

function status(text) { $("hudStatus").textContent = text ? `${(performance.now() / 1000 % 100).toFixed(2).padStart(5, "0")}  ${text}` : ""; }

// ---------- selection ----------
function select(i, fly) {
  S.sel = i;
  S.stDirty = true;
  S.neigh = new Set();
  if (i >= 0) {
    for (const l of S.adj[i]) S.neigh.add(l.s === i ? l.t : l.s);
    const n = S.nodes[i];
    if (fly !== false) { S.goal = [n.x, n.y, n.z]; S.goalDist = 520; }
    status(`${n.type} :: ${n.label} matched · ${n.title ? n.title.slice(0, 80) : S.g.repo.nameWithOwner}`);
  } else { S.goal = [0, 0, 0]; S.goalDist = 1100; }
  renderSelection();
  writeHash();
}

function writeHash() {
  const parts = [];
  if (S.sel >= 0) parts.push("node=" + encodeURIComponent(S.nodes[S.sel].id));
  if (S.tab) parts.push("tab=" + TABS[S.tab][0].toLowerCase());
  if (S.timeFrom !== -Infinity) parts.push("f=" + S.timeFrom);
  if (S.timeCut !== Infinity) parts.push("t=" + S.timeCut);
  history.replaceState(null, "", parts.length ? "#" + parts.join("&") : location.pathname);
}

function applyHash() {
  const h = new URLSearchParams(location.hash.slice(1));
  if (h.get("tab")) { const i = TABS.findIndex(([n]) => n.toLowerCase() === h.get("tab")); if (i >= 0) setTab(i); }
  if (h.get("f") || h.get("t")) setWindow(h.get("f") ? +h.get("f") : -Infinity, h.get("t") ? +h.get("t") : Infinity);
  if (h.get("q")) { $("q").value = h.get("q"); search(h.get("q")); }
  if (h.get("node") && S.byId.has(h.get("node"))) {
    const i = S.byId.get(h.get("node")), n = S.nodes[i];
    select(i, true);
    S.target = [n.x, n.y, n.z]; S.dist = 520;
  }
}

// ---------- history window (start / end handles and replay) ----------
function setRange() {
  const st = S.g.stats, span = Math.max(1, st.lastTime - st.firstTime);
  S.toT = (v) => st.firstTime + span * v / 1000;
  S.toV = (t) => Math.max(0, Math.min(1000, Math.round(1000 * (t - st.firstTime) / span)));
}

// New data can move the ends of the slider range; the handles keep their dates.
function rescale() {
  setRange();
  setWindow(S.timeFrom, S.timeCut);
  if (S.playing) startPlayClock();
}

function startPlayClock() {
  const fv = +$("timeFrom").value;
  S.playDur = Math.max(4000, 24000 * (1000 - fv) / 1000);
  S.playT0 = performance.now() - ((+$("time").value - fv) / Math.max(1, 1000 - fv)) * S.playDur;
}

function setupScrubber() {
  const fromEl = $("timeFrom"), toEl = $("time");
  setRange();
  const onInput = (which) => () => {
    stopPlay();
    if (which === "from" && +fromEl.value > +toEl.value) toEl.value = fromEl.value;
    if (which === "to" && +toEl.value < +fromEl.value) fromEl.value = toEl.value;
    readSliders();
  };
  fromEl.oninput = onInput("from");
  toEl.oninput = onInput("to");
  for (const el of [fromEl, toEl]) {
    el.addEventListener("pointerdown", () => { S.scrubbing = true; });
    el.addEventListener("pointerup", () => { S.scrubbing = false; S.panelDirty = true; });
  }
  $("play").onclick = () => {
    if (S.playing) { stopPlay(); return; }
    const fv = +fromEl.value;
    if (+toEl.value >= 1000 || +toEl.value <= fv) toEl.value = fv;
    startPlayClock();
    S.playing = true;
    $("play").textContent = "❚❚";
  };
  window.addEventListener("resize", drawSpark);
  updateLabels();
  drawSpark();
}

function readSliders() {
  const fv = +$("timeFrom").value, tv = +$("time").value;
  S.timeFrom = fv <= 0 ? -Infinity : Math.round(S.toT(fv));
  S.timeCut = tv >= 1000 ? Infinity : Math.round(S.toT(tv));
  windowChanged();
}

function setWindow(from, to) {
  S.timeFrom = from; S.timeCut = to;
  $("timeFrom").value = from === -Infinity ? 0 : S.toV(from);
  $("time").value = to === Infinity ? 1000 : S.toV(to);
  windowChanged(true);
}

function windowChanged(skipHash) {
  S.winDirty = true; S.panelDirty = true;
  updateLabels(); drawSpark();
  if (!skipHash && !S.playing) writeHash();
}

function stopPlay() {
  if (!S.playing) return;
  S.playing = false;
  $("play").textContent = "▶";
  S.panelDirty = true;
  writeHash();
}

function stepPlay(now) {
  const fv = +$("timeFrom").value;
  const v = Math.min(1000, fv + ((now - S.playT0) / S.playDur) * (1000 - fv));
  $("time").value = v;
  S.timeCut = v >= 1000 ? Infinity : Math.round(S.toT(v));
  windowChanged(true);
  if (v >= 1000) stopPlay();
}

function updateLabels() {
  $("fromLabel").textContent = S.timeFrom === -Infinity ? "START" : day(S.timeFrom);
  $("timeLabel").textContent = S.timeCut === Infinity ? "NOW" : day(S.timeCut);
  const fe = $("timeFrom"), te = $("time");
  fe.style.zIndex = +fe.value >= +te.value - 15 ? 3 : 1;
}

// Daily activity behind the handles; the part inside the window is bright.
function drawSpark() {
  if (!S.g) return;
  const st = S.g.stats, c = $("spark"), w = c.clientWidth, h = c.clientHeight, dpr = window.devicePixelRatio || 1;
  c.width = w * dpr; c.height = h * dpr;
  const x = c.getContext("2d"); x.scale(dpr, dpr);
  const days = st.days, max = Math.max(1, ...days.map((k) => k.events + k.commits));
  const span = Math.max(1, st.lastTime - st.firstTime), bw = Math.max(1, w / Math.max(1, days.length));
  for (const k of days) {
    const v = (k.events + k.commits) / max, px = ((k.t - st.firstTime) / span) * w;
    const inside = k.t + 86400 > S.timeFrom && k.t <= S.timeCut;
    x.fillStyle = inside ? "rgba(127,216,255,0.55)" : "rgba(127,216,255,0.14)";
    x.fillRect(px, h - v * h, Math.max(1, bw - 0.5), v * h);
  }
  x.fillStyle = "rgba(127,216,255,0.07)";
  const x0 = S.timeFrom === -Infinity ? 0 : ((S.timeFrom - st.firstTime) / span) * w, x1 = S.timeCut === Infinity ? w : ((S.timeCut - st.firstTime) / span) * w;
  x.fillRect(x0, 0, Math.max(0, x1 - x0), h);
}

function tickClock() {
  const s = Math.floor((performance.now() - S.started) / 1000);
  const p = (n) => String(n).padStart(2, "0");
  $("clock").textContent = `${p(Math.floor(s / 86400))}:${p(Math.floor(s / 3600) % 24)}:${p(Math.floor(s / 60) % 60)}:${p(s % 60)}`;
}

// ---------- GitHub links ----------
function githubUrl(n) {
  const base = S.g.repo.url, br = S.g.repo.defaultBranch, q = (s) => `${base}/issues?q=${encodeURIComponent(s)}`;
  const path = (p) => p.split("/").map(encodeURIComponent).join("/");
  switch (n.type) {
    case "repo": return base;
    case "issue": return `${base}/issues/${n.label.slice(1)}`;
    case "pr": return `${base}/pull/${n.label.slice(1)}`;
    case "commit": return `${base}/commit/${n.label}`;
    case "file": return `${base}/blob/${br}/${path(n.title)}`;
    case "dir": return `${base}/tree/${br}/${path(n.title.replace(/\/$/, ""))}`;
    case "label": return q(`label:"${n.label}"`);
    case "stage":
      if (n.id.startsWith("label:")) return q(`label:"${n.label}"`);
      return { opened: `${base}/issues`, closed: q("is:closed"), merged: `${base}/pulls?q=is:merged`, "not planned": q('is:closed reason:"not planned"') }[n.label] || base;
    case "milestone": return q(`milestone:"${n.label}"`);
    case "keyword": return q(n.label + " in:title");
    case "scope": return q(`${n.label} in:title`);
    case "user": return S.adj[n.i].some((l) => l.k !== "committed") ? `https://github.com/${n.label.slice(1)}` : "";
  }
  return "";
}

// ---------- panels ----------
// Writes html only when it differs from what the element already shows, keeps
// the scroll position, and marks the panel when a live refresh changed it.
function put(el, html) {
  if (el._html === html) return;
  const first = el._html === undefined, top = el.scrollTop;
  el._html = html;
  el.innerHTML = html;
  el.scrollTop = top;
  const panel = el.closest(".panel");
  if (panel && S.liveApply && !first) { panel.classList.remove("flash"); void panel.offsetWidth; panel.classList.add("flash"); }
}
function body(id) { return document.querySelector(`#${id} .body`); }
function nodeLink(id, text) { return S.byId.has(id) ? `<span class="link" data-node="${esc(id)}">${esc(text)}</span>` : esc(text); }
document.addEventListener("click", (e) => {
  const t = e.target.closest("[data-node]");
  if (t && S.byId.has(t.dataset.node)) select(S.byId.get(t.dataset.node), true);
});

// Window statistics are recomputed whenever the window moves, so every panel
// follows the replay.
function refreshPanels() {
  S.W = windowStats();
  S.SA = stageStats(S.W);
  renderOverview();
  renderSelection();
}

function stageStats(W) {
  const counts = new Map(), trans = new Map(), dwell = new Map(), variants = new Map();
  const bump = (m, k) => m.set(k, (m.get(k) || 0) + 1);
  for (const n of W.items) {
    const path = (n.meta.path || []).filter((p) => p.t <= W.to);
    if (!path.length) continue;
    const seen = new Set();
    path.forEach((p, k) => {
      if (!seen.has(p.s)) { seen.add(p.s); bump(counts, p.s); }
      if (k > 0) {
        bump(trans, path[k - 1].s + "\u0000" + p.s);
        const d = dwell.get(path[k - 1].s) || []; d.push((p.t - path[k - 1].t) / 3600); dwell.set(path[k - 1].s, d);
      }
    });
    bump(variants, path.map((p) => p.s).join("\u0000"));
  }
  const top = [...counts.entries()].sort((a, b) => b[1] - a[1]).slice(0, 7).map((e) => e[0])
    .sort((a, b) => (S.stageOrder.get(a) ?? 0.5) - (S.stageOrder.get(b) ?? 0.5));
  return { counts, trans, dwell, top, variants: [...variants.entries()].sort((a, b) => b[1] - a[1]).slice(0, 7) };
}

function flowMetrics(W) {
  const m = { issues: 0, issuesOpen: 0, prs: 0, prsOpen: 0, merged: 0, linked: 0, comments: 0 }, closeH = [], mergeH = [];
  for (const n of W.items) {
    m.comments += n.meta.comments || 0;
    if (n.type === "issue") {
      m.issues++;
      const c = n.meta.closedAt;
      if (c && c <= W.to) closeH.push((c - n.t) / 3600); else m.issuesOpen++;
      if (n.meta.byPR) m.linked++;
    } else {
      m.prs++;
      if (n.state === "MERGED" && n.meta.mergedAt <= W.to) { m.merged++; mergeH.push((n.meta.mergedAt - n.t) / 3600); }
      else if (!(n.meta.closedAt && n.meta.closedAt <= W.to)) m.prsOpen++;
    }
  }
  m.closeMed = median(closeH); m.mergeMed = median(mergeH);
  m.mergeRate = m.prs ? Math.round(1000 * m.merged / m.prs) / 10 : 0;
  m.linkedPct = m.issues ? Math.round(1000 * m.linked / m.issues) / 10 : 0;
  return m;
}

function renderOverview() {
  const g = S.g, W = S.W, st = g.stats, m = flowMetrics(W), range = rangeLabel(), [owner, name] = g.repo.nameWithOwner.split("/");
  S.M = m;
  $("p1r").textContent = `${fmt(W.items.length)} ITEMS`;
  put(body("p1"), `<div class="muted">${esc(owner)} /</div>
    <div class="big">${esc(name)}</div>
    <div style="margin:3px 0"><span style="color:#fde047">★ ${fmt(g.repo.stars)}</span> <span class="muted">· ${esc(g.repo.language || "")} · ${esc(g.repo.defaultBranch)}</span></div>
    <div class="ell muted">${esc(g.repo.description || "no description")}</div>
    <div class="acc" style="margin-top:5px">${range}</div>
    <div class="muted">${fmt(W.commits.length)} commits · open ${fmt(m.issuesOpen)} issues · ${fmt(m.prsOpen)} pulls</div>
    <div class="faint">synced ${ago(g.syncedAt)} · built ${ago(g.builtAt)}</div>`);

  const lc = new Map();
  for (const n of W.items) for (const l of n.meta.labels || []) lc.set(l, (lc.get(l) || 0) + 1);
  const labels = [...lc.entries()].sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1)).slice(0, 7), lmax = Math.max(1, ...labels.map((l) => l[1]));
  put(body("p2"), `<div class="bars">${labels.map(([l, c]) => `<div class="r ${isStage(l) ? "st" : ""}"><span class="n">${nodeLink("label:" + l, l.toUpperCase())}</span><span class="b"><i style="width:${(100 * c / lmax).toFixed(1)}%"></i></span><span class="c">${fmt(c)}</span></div>`).join("") || '<div class="muted">no items in this window</div>'}</div>`);

  put(body("p8"), activityPanel(W, m));
  $("p11r").textContent = range;
  put(body("p11"), `<div class="tiles">
    <div><div class="k">ISSUE CLOSE</div><div class="big">${m.closeMed ? dur(m.closeMed) : "—"}</div><div class="faint">median</div></div>
    <div><div class="k">PR TO MERGE</div><div class="big">${m.mergeMed ? dur(m.mergeMed) : "—"}</div><div class="faint">median</div></div>
    <div class="hi"><div class="k">MERGE RATE</div><div class="big">${m.mergeRate}%</div><div class="faint">${fmt(m.merged)} / ${fmt(m.prs)}</div></div>
    <div><div class="k">CLOSED BY PR</div><div class="big">${m.linkedPct}%</div><div class="faint">of ${fmt(m.issues)} issues</div></div></div>`);

  const counts = {};
  for (const n of S.nodes) if (S.vis[n.i]) counts[n.type] = (counts[n.type] || 0) + 1;
  for (const t in TYPES) { const el = $("lg-" + t); if (el) el.textContent = fmt(counts[t] || 0); }
  S.typeCount = Object.keys(counts).length;
}

function activityPanel(W, m) {
  const days = S.g.stats.days.filter((d) => d.t + 86400 > W.from && d.t <= W.to);
  const total = days.reduce((a, k) => a + k.events, 0), wk = days.slice(-60);
  const w = 300, h = 48, max = Math.max(1, ...wk.map((k) => k.events));
  const pts = wk.map((k, j) => `${(j * w / Math.max(1, wk.length - 1)).toFixed(1)},${(h - 2 - (k.events / max) * (h - 4)).toFixed(1)}`).join(" ");
  const types = [["ISS", m.issues], ["PRS", m.prs], ["MRG", m.merged], ["CMT", W.commits.length], ["CMNT", m.comments]];
  const tmax = Math.max(1, ...types.map((t) => t[1]));
  return `<div class="huge">${fmt(total)}</div><div class="muted">timeline events · ${fmt(days.length)} days</div>
    <svg viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" style="width:100%;height:44px;margin-top:4px">
      <polyline points="${pts}" fill="none" stroke="#7fd8ff" stroke-width="1.2" vector-effect="non-scaling-stroke"/></svg>
    <div style="display:flex;gap:6px;margin-top:2px">${types.map(([k, v]) => `<div style="flex:1"><div style="height:3px;background:${v === tmax ? "#7fd8ff" : "#3d5566"};width:${Math.max(8, 100 * v / tmax)}%"></div><div class="faint" style="font-size:9px">${k}</div></div>`).join("")}</div>`;
}

function renderSelection() {
  const i = S.sel, n = i >= 0 ? S.nodes[i] : null;
  renderInspector(n);
  renderCard(n);
  renderRadar(n);
  renderTree(n);
  renderLog(n);
  renderMatrix(n);
  renderTrajectory(n);
  renderKeywords(n);
  renderVariants(n);
}

function renderCard(n) {
  if (!n) return;
  $("calloutName").textContent = n.type === "issue" || n.type === "pr" ? n.label : n.label.slice(0, 32);
  const t = TYPES[n.type]?.name || n.type;
  const kind = n.type === "issue" || n.type === "pr" ? (n.state || "").toLowerCase() : n.type === "commit" ? `+${n.meta?.add ?? 0} −${n.meta?.del ?? 0}` : `${fmt(n.deg)} links`;
  const col = TYPES[n.type].color;
  $("card").style.borderColor = col + "88";
  put($("card"), `<div class="t">${esc(n.title || n.label)}</div>
    <div class="k">REPO</div><div>${esc(S.g.repo.nameWithOwner)}</div>
    <div class="k">KIND</div><div class="row"><span class="v" style="color:${col}">${esc(kind)}</span><span>${t}</span></div><div class="bar" style="background:${col};box-shadow:0 0 6px ${col}"></div>`);
}

function renderInspector(n) {
  const el = body("p3"), head = $("p3r");
  if (!n) {
    put(head, `<a class="ghbtn" href="${esc(S.g.repo.url)}" target="_blank" rel="noopener">OPEN REPO IN GITHUB ↗</a>`);
    put(el, `<div class="big">no selection</div><div class="muted" style="margin-top:6px">click a node, search with the prompt, or pick an entry from any panel.</div>
      <div class="faint" style="margin-top:8px">drag orbit · ctrl/middle-drag pan · wheel zoom · / search · r reset · esc clear</div>`);
    return;
  }
  const url = githubUrl(n);
  put(head, url ? `<a class="ghbtn" href="${esc(url)}" target="_blank" rel="noopener">OPEN IN GITHUB ↗</a>` : "TRACE");
  const m = n.meta || {}, kinds = {};
  for (const l of S.adj[n.i]) kinds[l.k] = (kinds[l.k] || 0) + 1;
  const rows = [["KIND", `${TYPES[n.type]?.name || n.type}${n.state ? " · " + n.state.toLowerCase() : ""}`]];
  if (m.author) rows.push(["AUTHOR", "@" + m.author]);
  if (n.t) rows.push(["FIRST", day(n.t)]);
  if (m.closedAt) rows.push(["CLOSED", day(m.closedAt)]);
  if (m.head) rows.push(["BRANCH", `${m.head} → ${m.base}`]);
  if (m.commits) rows.push(["CHURN", `${m.commits} commits · ${fmt(m.lines)} lines`]);
  if (m.dwellH !== undefined) rows.push(["DWELL", `${dur(m.dwellH)} median · ${fmt(m.items)} items`]);
  rows.push(["LINKS", Object.entries(kinds).sort((a, b) => b[1] - a[1]).slice(0, 4).map(([k, v]) => `${k} ${v}`).join(" · ")]);
  const labels = (m.labels || []).map((l) => `<span class="${isStage(l) ? "st" : ""}">${esc(l)}</span>`).join("");
  put(el, `<div class="ell" style="font-size:14px;margin-bottom:4px;color:${TYPES[n.type].color}">${esc(n.title || n.label)}</div>
    <dl class="kv">${rows.map(([k, v]) => `<dt>${k}</dt><dd>${esc(v)}</dd>`).join("")}</dl>
    <div class="chips" style="margin-top:5px">${labels}</div>`);
}

function isStage(l) { return S.byId.has("label:" + l) && S.nodes[S.byId.get("label:" + l)].type === "stage"; }

function renderRadar(n) {
  const nums = document.querySelector("#p4 .radarnums");
  if (!n) {
    S.blips = [...S.SA.counts.keys()].filter((s) => !["opened", "merged", "closed", "not planned"].includes(s)).slice(0, 14)
      .map((s, k) => ({ a: k * 0.45, r: 12 + 48 * (S.stageOrder.get(s) || 0.1), c: TYPES.stage.color }));
    put(nums, `<div class="k">STAGES</div><div class="huge">${String(S.SA.counts.size).padStart(2, "0")}</div><div class="k">NODE TYPES</div><div class="huge">${String(S.typeCount || 0).padStart(2, "0")}</div>`);
    return;
  }
  const types = {}, ids = [...S.neigh];
  for (const j of ids) types[S.nodes[j].type] = (types[S.nodes[j].type] || 0) + 1;
  const keys = Object.keys(TYPES);
  S.blips = ids.slice(0, 120).map((j) => {
    const t = keys.indexOf(S.nodes[j].type);
    return { a: (t / keys.length) * Math.PI * 2 + ((j * 0.618) % 0.4), r: 14 + 46 * Math.min(1, Math.log1p(S.nodes[j].deg) / 6), c: TYPES[S.nodes[j].type].color };
  });
  put(nums, `<div class="k">NEIGHBOURS</div><div class="huge">${String(ids.length).padStart(2, "0")}</div><div class="k">TYPES</div><div class="huge">${String(Object.keys(types).length).padStart(2, "0")}</div><div class="faint">found ${ids.length} / ${fmt(n.deg)}</div>`);
}

function renderTree(n) {
  const el = body("p5");
  const cnt = new Map();
  for (const c of S.W.commits) for (const l of S.adj[c.i]) if (l.k === "touches") { const f = S.nodes[l.s === c.i ? l.t : l.s]; cnt.set(f.title, (cnt.get(f.title) || 0) + 1); }
  let files = [...cnt.entries()].sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1)).slice(0, 18).map((e) => e[0]), on = null, label = "HOTSPOTS";
  if (n && ["commit", "dir", "pr", "issue", "scope"].includes(n.type)) {
    const set = new Set();
    const add = (j) => { const m = S.nodes[j]; if (m.type === "file") set.add(m.title); };
    for (const j of S.neigh) {
      add(j);
      if (n.type === "pr" || n.type === "issue") for (const l of S.adj[j]) if (S.nodes[j].type === "commit") add(l.s === j ? l.t : l.s);
    }
    if (n.type === "dir") files = [...cnt.keys()].filter((p) => p.startsWith(n.title));
    if (set.size) { files = [...set]; label = `${set.size} FILES`; }
  }
  if (n && n.type === "file") { on = n.title; files = [n.title, ...files.filter((p) => p !== n.title && p.split("/")[0] === n.title.split("/")[0])]; }
  $("p5r").textContent = label;
  const rows = [], seen = new Set();
  for (const p of files.slice(0, 18).sort()) {
    const parts = p.split("/");
    for (let d = 0; d < parts.length - 1; d++) {
      const dir = parts.slice(0, d + 1).join("/");
      if (seen.has(dir)) continue;
      seen.add(dir);
      rows.push(`<div class="r"><span>${"  ".repeat(d)}${nodeLink("dir:" + dir, parts[d] + "/")}</span><span></span></div>`);
    }
    const f = S.nodes[S.byId.get("file:" + p)];
    rows.push(`<div class="r ${p === on ? "on" : ""}"><span>${"  ".repeat(parts.length - 1)}├ ${nodeLink("file:" + p, parts[parts.length - 1])}</span><span>${cnt.get(p) ?? f?.meta?.commits ?? ""}</span></div>`);
  }
  put(el, rows.length ? `<div class="tree">${rows.slice(0, 9).join("")}</div>` : `<div class="muted">no commits in this window</div>`);
}

function renderLog(n) {
  let rows;
  if (n && n.meta && n.meta.events) {
    rows = n.meta.events.filter((e) => e[1] !== "comment").slice(-7).map((e) => [e[1], `${e[1].padEnd(9)} ${e[3] ?? ""}`, e[0]]);
  } else {
    const ev = S.events;
    let hi = ev.length;
    if (S.timeCut !== Infinity) { let lo = 0; hi = ev.length; while (lo < hi) { const mid = (lo + hi) >> 1; ev[mid].t <= S.timeCut ? lo = mid + 1 : hi = mid; } hi = lo; }
    const out = [];
    for (let k = hi - 1; k >= 0 && out.length < 7 && ev[k].t >= S.timeFrom; k--) out.push(ev[k]);
    rows = out.reverse().map((e) => {
      const node = S.nodes[e.node];
      const tail = e.type === "commit" ? (node.title || "").slice(0, 40) : (typeof e.x === "number" ? "#" + e.x : e.x) || (node.title || "").slice(0, 40);
      return [e.type, `${e.type.padEnd(9)} ${node.label} ${tail}`, e.t];
    });
  }
  put(body("p6"), rows.length ? `<div class="log">${rows.map((r, k) => `<div class="r"><span class="ln">${String(30 + k).padStart(2, "0")}</span><span class="op">${OPS[r[0]] || "·"}</span><span class="tx" title="${day(r[2])}">${esc(r[1])}</span></div>`).join("")}</div>` : `<div class="muted">no events in this window</div>`);
}

function renderMatrix(n) {
  const SA = S.SA, names = SA.top, idx = new Map(names.map((s, k) => [s, k]));
  const M = names.map(() => names.map(() => 0));
  for (const [key, c] of SA.trans) { const [a, b] = key.split("\u0000"); if (idx.has(a) && idx.has(b)) M[idx.get(a)][idx.get(b)] = c; }
  const max = Math.max(1, ...M.flat());
  const mine = new Set(), path = n?.meta?.path;
  if (path) for (let k = 1; k < path.length; k++) mine.add(path[k - 1].s + "→" + path[k].s);
  const abbr = (s) => s.replace(/^agent:/, "").replace("not planned", "n/p").slice(0, 5).toUpperCase();
  let html = `<div class="matrix" style="grid-template-columns:54px repeat(${names.length},1fr)"><span></span>${names.map((s) => `<span class="h">${abbr(s)}</span>`).join("")}`;
  names.forEach((from, r) => {
    html += `<span class="rh">${esc(from.replace(/^agent:/, ""))}</span>`;
    names.forEach((to, c) => {
      const v = M[r][c], a = v ? 0.12 + 0.88 * Math.sqrt(v / max) : 0;
      html += `<span class="c ${mine.has(from + "→" + to) ? "on" : ""}" title="${esc(from)} → ${esc(to)}: ${v}" style="background:rgba(34,211,238,${a.toFixed(2)})"></span>`;
    });
  });
  let total = 0; for (const c of SA.trans.values()) total += c;
  put(body("p7"), html + `</div><div class="faint" style="margin-top:4px">${fmt(total)} transitions · ${SA.counts.size} stages</div>`);
}

function renderTrajectory(n) {
  const el = body("p9"), path = n?.meta?.path?.filter((p) => p.t <= S.timeCut);
  if (!path) {
    $("p9r").textContent = "STAGES";
    put(el, `<div class="path">${S.SA.top.map((s) => `<div class="r"><b>▸</b>${nodeLink(S.stageId.get(s), s)}<span class="d">${fmt(S.SA.counts.get(s))} · ${S.SA.dwell.has(s) ? dur(median(S.SA.dwell.get(s))) : "—"}</span></div>`).join("") || '<div class="muted">no items in this window</div>'}</div>`);
    return;
  }
  $("p9r").textContent = `${path.length} STEPS`;
  put(el, `<div class="path">${path.slice(-8).map((p, k, a) => {
    const next = a[k + 1], d = next ? dur((next.t - p.t) / 3600) : "";
    const id = ["opened", "merged", "closed", "not planned"].includes(p.s) ? "stage:" + p.s : "label:" + p.s;
    return `<div class="r"><b>${k === a.length - 1 ? "◆" : "▸"}</b><span>${nodeLink(id, p.s)} <span class="faint">${day(p.t)}</span></span><span class="d">${d ? "+" + d : ""}</span></div>`;
  }).join("")}</div>`);
}

function renderKeywords(n) {
  const list = [];
  for (const k of S.kwNodes) {
    let c = 0;
    for (const l of S.adj[k.i]) { const o = S.nodes[l.s === k.i ? l.t : l.s]; if ((o.type === "issue" || o.type === "pr") && S.vis[o.i]) c++; }
    if (c) list.push({ term: k.label, c });
  }
  list.sort((a, b) => b.c - a.c || (a.term < b.term ? -1 : 1));
  const kws = list.slice(0, 32), max = Math.max(1, ...kws.map((k) => k.c));
  const mine = new Set(n?.meta?.keywords || (n?.type === "keyword" ? [n.label] : []));
  put(body("p10"), `<div class="kwbars">${kws.map((k) => `<i class="${mine.has(k.term) ? "on" : ""}" data-node="kw:${esc(k.term)}" title="${esc(k.term)} · ${k.c} items" style="height:${(100 * k.c / max).toFixed(1)}%"></i>`).join("")}</div>
    <div class="kwfoot"><span>${mine.size ? [...mine].join(" · ") : kws.slice(0, 4).map((k) => k.term).join(" · ")}</span><span class="muted">${list.length} terms</span></div>`);
}

function renderVariants(n) {
  const mine = n?.meta?.path?.filter((p) => p.t <= S.timeCut).map((p) => p.s).join("\u0000");
  put(body("p12"), `<div class="variants">${S.SA.variants.map(([key, c]) => {
    const path = key.split("\u0000");
    const p = path.map((s) => s.replace(/^agent:/, "")).map((s, k, a) => k === a.length - 1 ? `<b>${esc(s)}</b>` : esc(s)).join(" › ");
    return `<div class="r ${key === mine ? "on" : ""}"><span class="p" title="${esc(path.join(" → "))}">${p}</span><span class="c">${fmt(c)}</span></div>`;
  }).join("") || '<div class="muted">no items in this window</div>'}</div>`);
}
