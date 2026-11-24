"use strict";

// ---------- constants ----------
const TYPES = {
  repo:      { color: "#ffffff", size: 5,   name: "REPO" },
  stage:     { color: "#e9fbff", size: 3.2, name: "STAGE" },
  issue:     { color: "#7fd8ff", size: 1.5, name: "ISSUE" },
  pr:        { color: "#b9f2ff", size: 1.7, name: "PULL" },
  commit:    { color: "#3f8fcf", size: 1.2, name: "COMMIT" },
  file:      { color: "#4d7d9c", size: 1.1, name: "FILE" },
  dir:       { color: "#6fa9cf", size: 2.0, name: "DIR" },
  user:      { color: "#ffffff", size: 4,   name: "USER" },
  label:     { color: "#9fb8ff", size: 2.6, name: "LABEL" },
  keyword:   { color: "#5fd3c0", size: 1.8, name: "KEYWORD" },
  scope:     { color: "#8be0ff", size: 2.0, name: "SCOPE" },
  milestone: { color: "#c8d6ff", size: 2.4, name: "MILESTONE" },
};
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
  code:    ["rgba(70,140,200,", 0.10],
  flow:    ["rgba(190,235,255,", 0.035],
  social:  ["rgba(150,190,220,", 0.025],
  meaning: ["rgba(95,200,210,", 0.05],
  xref:    ["rgba(220,245,255,", 0.35],
};

// ---------- state ----------
const S = {
  g: null, nodes: [], links: [], adj: [], byId: new Map(),
  yaw: 0.6, pitch: -0.32, dist: 1100, target: [0, 0, 0], goal: null,
  sel: -1, hover: -1, tab: 0, query: "", matches: null,
  timeCut: Infinity, playing: false, lastInput: 0,
  proj: null, started: performance.now(), frames: 0, fps: 0, fpsT: performance.now(),
};
const $ = (id) => document.getElementById(id);
const fmt = (n) => n.toLocaleString("en-US");
const esc = (s) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const day = (t) => t ? new Date(t * 1000).toISOString().slice(0, 10) : "—";
const dur = (h) => h < 1 ? Math.round(h * 60) + "m" : h < 48 ? h.toFixed(1) + "h" : (h / 24).toFixed(1) + "d";
const ago = (iso) => { if (!iso) return "never"; const m = (Date.now() - Date.parse(iso)) / 60000; return m < 60 ? Math.round(m) + "m ago" : m < 2880 ? Math.round(m / 60) + "h ago" : Math.round(m / 1440) + "d ago"; };

// ---------- load ----------
fetch("graph.json").then((r) => { if (!r.ok) throw new Error("graph.json " + r.status + " — run: explorer build"); return r.json(); })
  .then(init).catch((e) => { $("sub").textContent = String(e); });

function init(g) {
  S.g = g;
  S.nodes = g.nodes;
  S.links = g.links;
  S.adj = S.nodes.map(() => []);
  S.nodes.forEach((n, i) => { S.byId.set(n.id, i); n.i = i; });
  for (const l of S.links) { S.adj[l.s].push(l); S.adj[l.t].push(l); l.g = LINK_GROUPS[l.k] || "meaning"; }
  // Normalise the layout so the 95th percentile radius is 320 units.
  const rs = S.nodes.map((n) => Math.hypot(...n.p)).sort((a, b) => a - b);
  const k = 320 / (rs[Math.floor(rs.length * 0.95)] || 1);
  for (const n of S.nodes) { n.x = n.p[0] * k; n.y = n.p[1] * k; n.z = n.p[2] * k; n.r = (TYPES[n.type]?.size || 1.5) * (1 + Math.sqrt(n.deg) * 0.12); }
  S.labelled = S.nodes.filter((n) => ["stage", "user", "repo"].includes(n.type) || (n.deg > 40 && ["label", "scope", "keyword", "dir"].includes(n.type)))
    .sort((a, b) => b.deg - a.deg).slice(0, 70).map((n) => n.i);
  S.proj = { x: new Float32Array(S.nodes.length), y: new Float32Array(S.nodes.length), s: new Float32Array(S.nodes.length), z: new Float32Array(S.nodes.length), v: new Uint8Array(S.nodes.length) };

  const c = g.stats.counts, r = g.repo;
  $("sub").textContent = `SRC github / ${r.nameWithOwner} / ${fmt(c.issue || 0)} issues / ${fmt(c.pr || 0)} pulls / ${fmt(c.commit || 0)} commits / ${fmt(c.file || 0)} files`;
  $("cmdrepo").textContent = r.nameWithOwner;
  $("synced").textContent = "SYNCED " + ago(g.syncedAt).toUpperCase();
  $("hudCount").textContent = `GRAPH / ${fmt(S.nodes.length)} NODES · ${fmt(S.links.length)} LINKS`;
  $("legend").innerHTML = ["issue", "pr", "commit", "file", "stage", "keyword", "user"].map((t) => `<i style="background:${TYPES[t].color};box-shadow:0 0 6px ${TYPES[t].color}"></i>${TYPES[t].name}`).join("");
  $("tabs").innerHTML = TABS.map(([n], i) => `<button data-i="${i}" class="${i === 0 ? "on" : ""}">${n}</button>`).join("");
  $("tabs").onclick = (e) => { const b = e.target.closest("button"); if (b) setTab(+b.dataset.i); };

  setupInput();
  setupScrubber();
  renderStatic();
  applyHash();
  renderSelection();
  requestAnimationFrame(frame);
}

// ---------- visibility ----------
function visible(n) {
  if (n.t && n.t > S.timeCut) return false;
  return true;
}
function inTab(n) {
  const types = TABS[S.tab][1];
  return !types || types.includes(n.type);
}

// ---------- camera / projection ----------
function project(w, h) {
  const P = S.proj, f = Math.min(w, h) * 1.15;
  const cy = Math.cos(S.yaw), sy = Math.sin(S.yaw), cp = Math.cos(S.pitch), sp = Math.sin(S.pitch);
  const [tx, ty, tz] = S.target;
  for (let i = 0; i < S.nodes.length; i++) {
    const n = S.nodes[i];
    if (!visible(n)) { P.v[i] = 0; continue; }
    const x = n.x - tx, y = n.y - ty, z = n.z - tz;
    const x1 = x * cy - z * sy, z1 = x * sy + z * cy;
    const y2 = y * cp - z1 * sp, z2 = y * sp + z1 * cp;
    const d = z2 + S.dist;
    if (d < 20) { P.v[i] = 0; continue; }
    const s = f / d;
    P.x[i] = w / 2 + x1 * s; P.y[i] = h / 2 + y2 * s; P.s[i] = s; P.z[i] = d; P.v[i] = 1;
  }
}

// ---------- rendering ----------
const canvas = $("graph"), ctx = canvas.getContext("2d");
const sprites = new Map();
function sprite(color) {
  if (sprites.has(color)) return sprites.get(color);
  const c = document.createElement("canvas"); c.width = c.height = 64;
  const x = c.getContext("2d"), gr = x.createRadialGradient(32, 32, 0, 32, 32, 32);
  gr.addColorStop(0, "rgba(255,255,255,1)"); gr.addColorStop(0.12, color); gr.addColorStop(0.35, color + "55"); gr.addColorStop(1, color + "00");
  x.fillStyle = gr; x.fillRect(0, 0, 64, 64);
  sprites.set(color, c);
  return c;
}

function frame(now) {
  const dpr = window.devicePixelRatio || 1, W = canvas.clientWidth, H = canvas.clientHeight;
  if (canvas.width !== W * dpr || canvas.height !== H * dpr) { canvas.width = W * dpr; canvas.height = H * dpr; }
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

  if (!S.drag && now - S.lastInput > 2500) S.yaw += 0.0009;
  if (S.goal) {
    for (let k = 0; k < 3; k++) S.target[k] += (S.goal[k] - S.target[k]) * 0.08;
    if (S.goalDist) S.dist += (S.goalDist - S.dist) * 0.06;
  }
  if (S.playing) stepPlay();

  project(W, H);
  ctx.clearRect(0, 0, W, H);
  ctx.fillStyle = "#04070b"; ctx.fillRect(0, 0, W, H);
  drawLinks();
  drawNodes();
  drawLabels(W, H);
  drawSelection(W, H);
  drawRadar(now);

  S.frames++;
  if (now - S.fpsT > 1000) { S.fps = Math.round(S.frames * 1000 / (now - S.fpsT)); S.frames = 0; S.fpsT = now; $("fps").textContent = S.fps + "FPS"; tickClock(); }
  $("hudCam").textContent = `AZ ${(((S.yaw * 180 / Math.PI) % 360 + 360) % 360).toFixed(1)}° Z ${Math.round(S.dist / 4)}`;
  requestAnimationFrame(frame);
}

function nodeAlpha(i) {
  const n = S.nodes[i];
  let a = Math.max(0.25, Math.min(1, 900 / S.proj.z[i]));
  if (!inTab(n)) a *= 0.08;
  if (S.matches && !S.matches.has(i)) a *= 0.12;
  if (S.sel >= 0 && i !== S.sel && !S.neigh.has(i)) a *= 0.35;
  return a;
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
      (l.g === "xref" || l.g === "flow" ? x : p).moveTo(P.x[l.s], P.y[l.s]);
      (l.g === "xref" || l.g === "flow" ? x : p).lineTo(P.x[l.t], P.y[l.t]);
    }
    ctx.lineWidth = 0.9; ctx.strokeStyle = "rgba(127,216,255,0.55)"; ctx.stroke(p);
    ctx.lineWidth = 1.4; ctx.strokeStyle = "rgba(235,252,255,0.9)"; ctx.stroke(x);
  }
}

function drawNodes() {
  const P = S.proj;
  const order = [];
  for (let i = 0; i < S.nodes.length; i++) if (P.v[i]) order.push(i);
  order.sort((a, b) => P.z[b] - P.z[a]);
  ctx.globalCompositeOperation = "lighter";
  for (const i of order) {
    const n = S.nodes[i], t = TYPES[n.type] || TYPES.file;
    const a = nodeAlpha(i);
    if (a < 0.02) continue;
    const hot = i === S.hover || (S.matches && S.matches.has(i)) || i === S.sel || S.neigh?.has(i);
    const r = Math.max(0.7, n.r * P.s[i]) * (hot ? 1.6 : 1);
    const hub = n.deg > 25;
    ctx.globalAlpha = a * (hot ? 0.9 : hub ? 0.6 : 0.32);
    const g = Math.min(hub ? 30 : 16, r * (hub ? 4 : 3.2));
    ctx.drawImage(sprite(t.color), P.x[i] - g, P.y[i] - g, g * 2, g * 2);
    ctx.globalAlpha = a * 0.9;
    ctx.fillStyle = t.color;
    const c = Math.min(2.4, r * 0.55);
    ctx.fillRect(P.x[i] - c / 2, P.y[i] - c / 2, c, c);
  }
  ctx.globalAlpha = 1;
  ctx.globalCompositeOperation = "source-over";
}

function drawLabels(W, H) {
  const P = S.proj;
  ctx.font = "10px " + getComputedStyle(document.body).fontFamily;
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
    ctx.fillStyle = n.type === "stage" ? "rgba(225,245,255,0.85)" : "rgba(160,190,210,0.7)";
    ctx.fillText(shortLabel(n), x + 7, y - 7);
  }
}

function shortLabel(n) {
  if (n.type === "issue" || n.type === "pr") return n.label + (S.sel >= 0 ? " " + (n.title || "").slice(0, 34) : "");
  return n.label.length > 28 ? n.label.slice(0, 27) + "…" : n.label;
}

function drawSelection(W, H) {
  const callout = $("callout"), card = $("card");
  if (S.sel < 0 || !S.proj.v[S.sel]) { callout.hidden = card.hidden = true; return; }
  const P = S.proj, i = S.sel, x = P.x[i], y = P.y[i];
  ctx.strokeStyle = "rgba(200,240,255,0.55)"; ctx.lineWidth = 1;
  ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(W, y); ctx.stroke();
  ctx.strokeStyle = "rgba(127,216,255,0.35)";
  ctx.beginPath(); ctx.arc(x, y, Math.min(W, H) * 0.24, 0, Math.PI * 2); ctx.stroke();
  ctx.globalCompositeOperation = "lighter";
  const g = 26;
  ctx.drawImage(sprite("#ffffff"), x - g, y - g, g * 2, g * 2);
  ctx.globalCompositeOperation = "source-over";
  callout.hidden = card.hidden = false;
  const cx = Math.min(W - callout.offsetWidth - 10, x + 30), cy = Math.max(8, y - 44);
  callout.style.transform = `translate(${cx}px,${cy}px)`;
  const kx = Math.max(10, Math.min(W - 290, x - 290)), ky = Math.min(H - card.offsetHeight - 30, y + 36);
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
    rctx.fillStyle = `rgba(190,240,255,${Math.max(0.25, 1 - age / 4)})`;
    rctx.fillRect(c + Math.cos(b.a) * b.r - 2, c + Math.sin(b.a) * b.r - 2, 4, 4);
  }
}

// ---------- input ----------
function setupInput() {
  const stage = $("stage");
  let down = null, moved = false;
  stage.addEventListener("pointerdown", (e) => { down = [e.clientX, e.clientY, S.yaw, S.pitch]; moved = false; S.drag = true; stage.classList.add("drag"); stage.setPointerCapture(e.pointerId); });
  stage.addEventListener("pointermove", (e) => {
    S.lastInput = performance.now();
    const rect = canvas.getBoundingClientRect(), mx = e.clientX - rect.left, my = e.clientY - rect.top;
    if (down) {
      const dx = e.clientX - down[0], dy = e.clientY - down[1];
      if (Math.abs(dx) + Math.abs(dy) > 3) moved = true;
      S.yaw = down[2] + dx * 0.005;
      S.pitch = Math.max(-1.5, Math.min(1.5, down[3] + dy * 0.005));
      return;
    }
    S.hover = pick(mx, my);
    const tip = $("tip");
    if (S.hover >= 0) {
      const n = S.nodes[S.hover];
      tip.hidden = false;
      tip.textContent = `${TYPES[n.type]?.name || n.type} ${n.label}${n.title && n.title !== n.label ? " · " + n.title : ""}`;
      tip.style.transform = `translate(${Math.min(mx + 14, rect.width - 300)}px,${my + 14}px)`;
    } else tip.hidden = true;
  });
  stage.addEventListener("pointerup", (e) => {
    stage.classList.remove("drag"); S.drag = false;
    if (down && !moved) {
      const rect = canvas.getBoundingClientRect();
      const i = pick(e.clientX - rect.left, e.clientY - rect.top);
      select(i, false);
    }
    down = null;
  });
  stage.addEventListener("wheel", (e) => { e.preventDefault(); S.lastInput = performance.now(); S.goalDist = 0; S.dist = Math.max(60, Math.min(4000, S.dist * Math.exp(e.deltaY * 0.001))); }, { passive: false });
  stage.addEventListener("pointerleave", () => { S.hover = -1; $("tip").hidden = true; });

  const q = $("q");
  q.addEventListener("input", () => search(q.value));
  q.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && S.matches && S.matches.size) {
      const best = [...S.matches].sort((a, b) => scoreMatch(b) - scoreMatch(a))[0];
      select(best, true);
    }
    if (e.key === "Escape") { q.value = ""; search(""); select(-1); }
  });
  window.addEventListener("keydown", (e) => {
    if (e.key === "/" && document.activeElement !== q) { e.preventDefault(); q.focus(); }
    if (e.key === "Escape" && document.activeElement !== q) select(-1);
  });
}

function pick(mx, my) {
  const P = S.proj;
  let best = -1, bd = 1e9;
  for (let i = 0; i < S.nodes.length; i++) {
    if (!P.v[i] || nodeAlpha(i) < 0.05) continue;
    const dx = P.x[i] - mx, dy = P.y[i] - my, d = dx * dx + dy * dy;
    const r = Math.max(7, S.nodes[i].r * P.s[i] * 3);
    if (d < r * r && d + P.z[i] * 1e-4 < bd) { bd = d + P.z[i] * 1e-4; best = i; }
  }
  return best;
}

function scoreMatch(i) {
  const n = S.nodes[i], q = S.query;
  let s = n.deg * 0.01;
  if (n.label.toLowerCase() === q || n.label.toLowerCase() === "#" + q || n.label.toLowerCase() === "@" + q) s += 100;
  if (n.label.toLowerCase().startsWith(q)) s += 10;
  return s;
}

function search(text) {
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
  [...$("tabs").children].forEach((b, k) => b.classList.toggle("on", k === i));
  status(`view :: ${TABS[i][0].toLowerCase()} · ${TABS[i][1] ? TABS[i][1].join(", ") : "every node type"}`);
  writeHash();
}

function status(text) { $("hudStatus").textContent = text ? `${(performance.now() / 1000 % 100).toFixed(2).padStart(5, "0")}  ${text}` : ""; }

// ---------- selection ----------
function select(i, fly) {
  S.sel = i;
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
  if (S.timeCut !== Infinity) parts.push("t=" + S.timeCut);
  history.replaceState(null, "", parts.length ? "#" + parts.join("&") : location.pathname);
}

function applyHash() {
  const h = new URLSearchParams(location.hash.slice(1));
  if (h.get("tab")) { const i = TABS.findIndex(([n]) => n.toLowerCase() === h.get("tab")); if (i >= 0) setTab(i); }
  if (h.get("t")) setTime(+h.get("t"));
  if (h.get("q")) { $("q").value = h.get("q"); search(h.get("q")); }
  if (h.get("node") && S.byId.has(h.get("node"))) {
    const i = S.byId.get(h.get("node")), n = S.nodes[i];
    select(i, true);
    S.target = [n.x, n.y, n.z]; S.dist = 520;
  }
}

// ---------- history scrubber ----------
function setupScrubber() {
  const st = S.g.stats, el = $("time");
  const span = Math.max(1, st.lastTime - st.firstTime);
  S.toT = (v) => st.firstTime + span * v / 1000;
  el.oninput = () => { S.playing = false; $("play").textContent = "▶"; setTime(+el.value >= 1000 ? Infinity : Math.round(S.toT(+el.value))); };
  $("play").onclick = () => {
    S.playing = !S.playing;
    $("play").textContent = S.playing ? "❚❚" : "▶";
    if (S.playing && +el.value >= 1000) { el.value = 0; }
    S.playFrom = performance.now() - (+el.value / 1000) * 24000;
  };
  // Activity sparkline behind the slider.
  const c = $("spark"), w = c.clientWidth, h = c.clientHeight, dpr = window.devicePixelRatio || 1;
  c.width = w * dpr; c.height = h * dpr;
  const x = c.getContext("2d"); x.scale(dpr, dpr);
  const days = st.days.filter((d) => d.t >= st.firstTime - 86400), max = Math.max(1, ...days.map((k) => k.events + k.commits));
  const bw = w / Math.max(1, days.length);
  days.forEach((k, j) => {
    const v = (k.events + k.commits) / max;
    x.fillStyle = "rgba(127,216,255,0.28)";
    x.fillRect(j * bw + 0.5, h - v * h, Math.max(1, bw - 1), v * h);
  });
}

function stepPlay() {
  const el = $("time");
  const v = Math.min(1000, ((performance.now() - S.playFrom) / 24000) * 1000);
  el.value = v;
  setTime(v >= 1000 ? Infinity : Math.round(S.toT(v)), true);
  if (v >= 1000) { S.playing = false; $("play").textContent = "▶"; }
}

function setTime(t, quiet) {
  S.timeCut = t;
  if (t !== Infinity) {
    const el = $("time"), st = S.g.stats;
    if (!S.playing) el.value = Math.round(1000 * (t - st.firstTime) / Math.max(1, st.lastTime - st.firstTime));
  }
  $("timeLabel").textContent = t === Infinity ? "NOW" : day(t);
  if (!quiet) writeHash();
}

function tickClock() {
  const s = Math.floor((performance.now() - S.started) / 1000);
  const p = (n) => String(n).padStart(2, "0");
  $("clock").textContent = `${p(Math.floor(s / 86400))}:${p(Math.floor(s / 3600) % 24)}:${p(Math.floor(s / 60) % 60)}:${p(s % 60)}`;
}

// ---------- panels ----------
function body(id) { return document.querySelector(`#${id} .body`); }
function nodeLink(id, text) { return S.byId.has(id) ? `<span class="link" data-node="${esc(id)}">${esc(text)}</span>` : esc(text); }
document.addEventListener("click", (e) => {
  const t = e.target.closest("[data-node]");
  if (t && S.byId.has(t.dataset.node)) select(S.byId.get(t.dataset.node), true);
});

function renderStatic() {
  const g = S.g, st = g.stats, m = st.metrics, [owner, name] = g.repo.nameWithOwner.split("/");
  $("p1r").textContent = `${fmt(m.issues + m.prs)} ITEMS`;
  body("p1").innerHTML = `<div class="muted">${esc(owner)} /</div>
    <div class="big">${esc(name)}</div>
    <div style="margin:4px 0"><span class="acc">★ ${fmt(g.repo.stars)}</span> <span class="muted">· ${esc(g.repo.language || "")} · ${esc(g.repo.defaultBranch)}</span></div>
    <div class="ell muted">${esc(g.repo.description || "no description")}</div>
    <div class="muted" style="margin-top:6px">open ${fmt(m.issuesOpen)} issues · ${fmt(m.prsOpen)} pulls</div>
    <div class="faint">synced ${ago(g.syncedAt)} · built ${ago(g.builtAt)}</div>`;

  const labels = st.labels.slice(0, 7), lmax = Math.max(1, ...labels.map((l) => l.count));
  body("p2").innerHTML = `<div class="bars">${labels.map((l) => `<div class="r ${l.stage ? "st" : ""}"><span class="n">${nodeLink((l.stage ? "label:" : "label:") + l.name, l.name.toUpperCase())}</span><span class="b"><i style="width:${(100 * l.count / lmax).toFixed(1)}%"></i></span><span class="c">${fmt(l.count)}</span></div>`).join("")}</div>`;

  body("p8").innerHTML = activityPanel();
  body("p11").innerHTML = `<div class="tiles">
    <div><div class="k">ISSUE CLOSE</div><div class="big">${dur(m.issueCloseMedianH)}</div><div class="faint">median</div></div>
    <div><div class="k">PR TO MERGE</div><div class="big">${dur(m.prMergeMedianH)}</div><div class="faint">median</div></div>
    <div class="hi"><div class="k">MERGE RATE</div><div class="big">${m.mergeRate}%</div><div class="faint">${fmt(m.prsMerged)} / ${fmt(m.prs)}</div></div>
    <div><div class="k">CLOSED BY PR</div><div class="big">${m.issuesLinkedPct}%</div><div class="faint">of issues</div></div></div>`;
}

function activityPanel() {
  const st = S.g.stats, wk = st.days.slice(-60), m = st.metrics;
  const total = st.days.reduce((a, k) => a + k.events, 0);
  const w = 300, h = 48, max = Math.max(1, ...wk.map((k) => k.events));
  const pts = wk.map((k, j) => `${(j * w / Math.max(1, wk.length - 1)).toFixed(1)},${(h - 2 - (k.events / max) * (h - 4)).toFixed(1)}`).join(" ");
  const types = [["ISS", m.issues], ["PRS", m.prs], ["MRG", m.prsMerged], ["CMT", m.commits], ["CMNT", m.comments]];
  const tmax = Math.max(...types.map((t) => t[1]));
  return `<div class="huge">${fmt(total)}</div><div class="muted">timeline events · last 60 days below</div>
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
  $("card").innerHTML = `<div class="t">${esc(n.title || n.label)}</div>
    <div class="k">REPO</div><div>${esc(S.g.repo.nameWithOwner)}</div>
    <div class="k">KIND</div><div class="row"><span class="v">${esc(kind)}</span><span>${t}</span></div><div class="bar"></div>`;
}

function renderInspector(n) {
  const el = body("p3");
  if (!n) {
    el.innerHTML = `<div class="big">no selection</div><div class="muted" style="margin-top:6px">click a node, search with the prompt, or pick an entry from any panel.</div>
      <div class="faint" style="margin-top:8px">drag to orbit · wheel to zoom · / to search · esc to clear</div>`;
    return;
  }
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
  el.innerHTML = `<div class="ell" style="font-size:14px;margin-bottom:4px">${esc(n.title || n.label)}</div>
    <dl class="kv">${rows.map(([k, v]) => `<dt>${k}</dt><dd>${esc(v)}</dd>`).join("")}</dl>
    <div class="chips" style="margin-top:5px">${labels}</div>`;
}

function isStage(l) { return S.byId.has("label:" + l) && S.nodes[S.byId.get("label:" + l)].type === "stage"; }

function renderRadar(n) {
  const nums = document.querySelector("#p4 .radarnums");
  if (!n) {
    const c = S.g.stats.counts;
    S.blips = S.g.workflow.stages.filter((s) => !s.synthetic).slice(0, 14).map((s, k) => ({ a: k * 0.45, r: 12 + 48 * (s.order || 0.1) }));
    nums.innerHTML = `<div class="k">STAGES</div><div class="huge">${String(S.g.workflow.stages.length).padStart(2, "0")}</div><div class="k">NODE TYPES</div><div class="huge">${String(Object.keys(c).length).padStart(2, "0")}</div>`;
    return;
  }
  const types = {}, ids = [...S.neigh];
  for (const j of ids) types[S.nodes[j].type] = (types[S.nodes[j].type] || 0) + 1;
  const keys = Object.keys(TYPES);
  S.blips = ids.slice(0, 120).map((j) => {
    const t = keys.indexOf(S.nodes[j].type);
    return { a: (t / keys.length) * Math.PI * 2 + ((j * 0.618) % 0.4), r: 14 + 46 * Math.min(1, Math.log1p(S.nodes[j].deg) / 6) };
  });
  nums.innerHTML = `<div class="k">NEIGHBOURS</div><div class="huge">${String(ids.length).padStart(2, "0")}</div><div class="k">TYPES</div><div class="huge">${String(Object.keys(types).length).padStart(2, "0")}</div><div class="faint">found ${ids.length} / ${fmt(n.deg)}</div>`;
}

function renderTree(n) {
  const el = body("p5");
  let files = S.g.stats.hotspots.map((h) => h.path), on = null, label = "HOTSPOTS";
  if (n && (n.type === "commit" || n.type === "dir" || n.type === "pr" || n.type === "issue" || n.type === "scope")) {
    const set = new Set();
    const add = (j) => { const m = S.nodes[j]; if (m.type === "file") set.add(m.title); };
    for (const j of S.neigh) {
      add(j);
      if (n.type === "pr" || n.type === "issue") for (const l of S.adj[j]) if (S.nodes[j].type === "commit") add(l.s === j ? l.t : l.s);
    }
    if (n.type === "dir") files = files.filter((p) => p.startsWith(n.title));
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
    rows.push(`<div class="r ${p === on ? "on" : ""}"><span>${"  ".repeat(parts.length - 1)}├ ${nodeLink("file:" + p, parts[parts.length - 1])}</span><span>${f?.meta?.commits ?? ""}</span></div>`);
  }
  el.innerHTML = `<div class="tree">${rows.slice(0, 9).join("")}</div>`;
}

const OPS = { labeled: "+", unlabeled: "−", closed: "×", reopened: "↺", merged: "⇢", xref: "↔", connected: "↔", assigned: "@", review: "✓", duplicate: "≡", comment: "»" };
function renderLog(n) {
  let rows;
  if (n && n.meta && n.meta.events) {
    rows = n.meta.events.filter((e) => e[1] !== "comment").slice(-7).map((e) => [e[1], `${e[1].padEnd(9)} ${e[3] ?? ""}`, e[0]]);
  } else {
    rows = S.g.stats.recent.slice(0, 7).reverse().map((e) => {
      const node = S.nodes[S.byId.get(e.node)];
      return [e.type, `${e.type.padEnd(9)} ${node?.label ?? ""} ${e.label || e.state || (node?.title ?? "").slice(0, 40)}`, e.t];
    });
  }
  body("p6").innerHTML = `<div class="log">${rows.map((r, k) => `<div class="r"><span class="ln">${String(30 + k).padStart(2, "0")}</span><span class="op">${OPS[r[0]] || "·"}</span><span class="tx" title="${day(r[2])}">${esc(r[1])}</span></div>`).join("")}</div>`;
}

function topStages() {
  return S.g.workflow.stages.slice().sort((a, b) => b.items - a.items).slice(0, 7).sort((a, b) => a.order - b.order);
}

function renderMatrix(n) {
  const st = topStages(), names = st.map((s) => s.name), idx = new Map(names.map((s, k) => [s, k]));
  const M = names.map(() => names.map(() => 0));
  for (const t of S.g.workflow.transitions) if (idx.has(t.from) && idx.has(t.to)) M[idx.get(t.from)][idx.get(t.to)] = t.count;
  const max = Math.max(1, ...M.flat());
  const mine = new Set();
  const path = n?.meta?.path;
  if (path) for (let k = 1; k < path.length; k++) mine.add(path[k - 1].s + "→" + path[k].s);
  const abbr = (s) => s.replace(/^agent:/, "").replace("not planned", "n/p").slice(0, 5).toUpperCase();
  let html = `<div class="matrix" style="grid-template-columns:62px repeat(${names.length},1fr)"><span></span>${names.map((s) => `<span class="h">${abbr(s)}</span>`).join("")}`;
  names.forEach((from, r) => {
    html += `<span class="rh">${esc(from.replace(/^agent:/, ""))}</span>`;
    names.forEach((to, c) => {
      const v = M[r][c], a = v ? 0.12 + 0.88 * Math.sqrt(v / max) : 0;
      html += `<span class="c ${mine.has(from + "→" + to) ? "on" : ""}" title="${esc(from)} → ${esc(to)}: ${v}" style="background:rgba(127,216,255,${a.toFixed(2)})"></span>`;
    });
  });
  body("p7").innerHTML = html + `</div><div class="faint" style="margin-top:4px">${fmt(S.g.workflow.transitions.reduce((a, t) => a + t.count, 0))} transitions · ${S.g.workflow.stages.length} stages inferred</div>`;
}

function renderTrajectory(n) {
  const el = body("p9");
  const path = n?.meta?.path;
  if (!path) {
    $("p9r").textContent = "STAGES";
    el.innerHTML = `<div class="path">${topStages().map((s) => `<div class="r"><b>▸</b>${nodeLink(s.id, s.name)}<span class="d">${fmt(s.items)} · ${s.medianDwellH ? dur(s.medianDwellH) : "—"}</span></div>`).join("")}</div>`;
    return;
  }
  $("p9r").textContent = `${path.length} STEPS`;
  el.innerHTML = `<div class="path">${path.slice(-8).map((p, k, a) => {
    const next = a[k + 1], d = next ? dur((next.t - p.t) / 3600) : "";
    const id = ["opened", "merged", "closed", "not planned"].includes(p.s) ? "stage:" + p.s : "label:" + p.s;
    return `<div class="r"><b>${k === a.length - 1 ? "◆" : "▸"}</b><span>${nodeLink(id, p.s)} <span class="faint">${day(p.t)}</span></span><span class="d">${d ? "+" + d : ""}</span></div>`;
  }).join("")}</div>`;
}

function renderKeywords(n) {
  const kws = S.g.stats.keywords.slice(0, 32), max = Math.max(1, ...kws.map((k) => k.df));
  const mine = new Set(n?.meta?.keywords || (n?.type === "keyword" ? [n.label] : []));
  body("p10").innerHTML = `<div class="kwbars">${kws.map((k) => `<i class="${mine.has(k.term) ? "on" : ""}" data-node="kw:${esc(k.term)}" title="${esc(k.term)} · ${k.df} titles" style="height:${(100 * k.df / max).toFixed(1)}%"></i>`).join("")}</div>
    <div class="kwfoot"><span>${mine.size ? [...mine].join(" · ") : kws.slice(0, 4).map((k) => k.term).join(" · ")}</span><span class="muted">${S.g.stats.counts.keyword} terms</span></div>`;
}

function renderVariants(n) {
  const mine = n?.meta?.path?.map((p) => p.s).join(">");
  body("p12").innerHTML = `<div class="variants">${S.g.workflow.variants.slice(0, 7).map((v) => {
    const p = v.path.map((s) => s.replace(/^agent:/, "")).map((s, k, a) => k === a.length - 1 ? `<b>${esc(s)}</b>` : esc(s)).join(" › ");
    return `<div class="r ${v.path.join(">") === mine ? "on" : ""}"><span class="p" title="${esc(v.path.join(" → "))}">${p}</span><span class="c">${fmt(v.count)}</span></div>`;
  }).join("")}</div>`;
}
