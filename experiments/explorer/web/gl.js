"use strict";

// WebGL2 renderer for the graph layer: all links in one draw call and all
// nodes as point sprites whose shape is computed in the fragment shader.
// Positions and per-node state live in float textures, so the camera, window,
// tab, search and selection are applied on the GPU and the CPU does no
// per-frame work for the 3,000+ nodes and 25,000+ links. Labels, selection
// callout and radar stay on the 2D canvas above it (see app.js).

const SHAPES = ["circle", "diamond", "square", "box", "triangle", "down", "plus", "hexagon", "pentagon", "star"];
const GROUPS = ["code", "flow", "social", "meaning", "xref"];
const TEX_W = 1024;

const COMMON = `#version 300 es
precision highp float;
precision highp int;
uniform sampler2D u_pos;
uniform sampler2D u_st;
uniform vec3 u_target;
uniform vec4 u_cam;
uniform float u_dist;
uniform float u_f;
uniform vec2 u_res;
uniform int u_sel;
uniform bool u_hasSel;
vec4 fetch(sampler2D t, int i) { return texelFetch(t, ivec2(i % ${TEX_W}, i / ${TEX_W}), 0); }
// xy: clip position, z: depth, w: pixels per unit
vec4 proj(vec3 p) {
  vec3 q = p - u_target;
  float x1 = q.x * u_cam.x - q.z * u_cam.y;
  float z1 = q.x * u_cam.y + q.z * u_cam.x;
  float y2 = q.y * u_cam.z - z1 * u_cam.w;
  float z2 = q.y * u_cam.w + z1 * u_cam.z;
  float d = z2 + u_dist;
  float s = u_f / d;
  return vec4(x1 * s * 2.0 / u_res.x, -y2 * s * 2.0 / u_res.y, d, s);
}
`;

const LINK_VS = COMMON + `
in ivec3 a_link;
uniform int u_pass;
uniform bool u_query;
uniform float u_dim;
uniform vec4 u_grp[5];
out vec4 v_col;
void main() {
  int i = a_link.x, j = a_link.y, g = a_link.z;
  vec4 si = fetch(u_st, i), sj = fetch(u_st, j);
  vec4 A = proj(fetch(u_pos, i).xyz), B = proj(fetch(u_pos, j).xyz);
  bool ok = si.x > 0.5 && sj.x > 0.5 && A.z >= 20.0 && B.z >= 20.0;
  bool adj = i == u_sel || j == u_sel;
  if (u_pass == 0) {
    ok = ok && si.y > 0.5 && sj.y > 0.5;
    if (u_query) ok = ok && ((si.z > 0.5 && si.z < 1.5) || (sj.z > 0.5 && sj.z < 1.5));
  } else ok = ok && adj;
  if (!ok) { gl_Position = vec4(2.0, 2.0, 2.0, 1.0); v_col = vec4(0.0); return; }
  gl_Position = vec4(A.xy, 0.0, 1.0);
  if (u_pass == 0) { v_col = u_grp[g]; v_col.a *= u_dim * 0.6; }
  else v_col = (g == 1 || g == 4) ? vec4(0.92, 0.99, 1.0, 0.9) : vec4(0.5, 0.85, 1.0, 0.55);
}`;

const LINK_FS = `#version 300 es
precision highp float;
in vec4 v_col;
out vec4 o;
void main() { o = v_col; }`;

const NODE_VS = COMMON + `
in vec4 a_meta; // type, radius, outlined, glow
uniform int u_pass; // 0 glow, 1 shapes
uniform int u_hover;
uniform float u_dpr;
uniform float u_maxPt;
uniform vec4 u_ty[12];  // rgb, shape
uniform float u_min[12];
out vec3 v_col;
out float v_alpha;
out float v_r;
out float v_h;
out float v_shape;
out float v_out;
out float v_hot;
void hide() { gl_Position = vec4(2.0, 2.0, 2.0, 1.0); gl_PointSize = 0.0; v_alpha = 0.0; }
void main() {
  int i = gl_VertexID, ty = int(a_meta.x);
  vec4 st = fetch(u_st, i);
  vec4 A = proj(fetch(u_pos, i).xyz);
  if (st.x < 0.5 || A.z < 20.0) { hide(); return; }
  float a = clamp(1000.0 / A.z, 0.35, 1.0);
  if (st.y < 0.5) a *= 0.1;
  if (st.z > 1.5) a *= 0.15;
  if (u_hasSel && st.w < 0.5) a *= 0.4;
  float r = min(16.0, max(u_min[ty], a_meta.y * A.w * 1.15));
  vec4 ty4 = u_ty[ty];
  v_col = ty4.rgb;
  v_shape = ty4.w;
  gl_Position = vec4(A.xy, 0.0, 1.0);
  if (u_pass == 0) {
    if (a_meta.w < 0.5 || a <= 0.3) { hide(); return; }
    float g = min(34.0, r * 3.2);
    v_h = g;
    gl_PointSize = min(2.0 * g * u_dpr, u_maxPt);
    v_alpha = 1.0;
    return;
  }
  if (a < 0.03) { hide(); return; }
  bool top = i == u_hover || i == u_sel;
  bool hot = top || (st.z > 0.5 && st.z < 1.5) || (u_hasSel && st.w > 0.5);
  if (hot) r *= top ? 1.7 : 1.35;
  float h = r * 1.3 + 3.0;
  gl_PointSize = min(2.0 * h * u_dpr, u_maxPt);
  float hh = gl_PointSize / (2.0 * u_dpr);
  v_r = r * min(1.0, (hh - 3.0) / (r * 1.3));
  v_h = hh;
  v_hot = hot ? 1.0 : 0.0;
  v_out = a_meta.z;
  v_alpha = hot ? 1.0 : max(0.12, a) * (a_meta.z > 0.5 ? 0.95 : 0.9);
}`;

const NODE_FS = `#version 300 es
precision highp float;
precision highp int;
in vec3 v_col;
in float v_alpha;
in float v_r;
in float v_h;
in float v_shape;
in float v_out;
in float v_hot;
uniform int u_pass;
uniform float u_dpr;
out vec4 o;
float box(vec2 p, vec2 b) { vec2 d = abs(p) - b; return length(max(d, 0.0)) + min(max(d.x, d.y), 0.0); }
float tri(vec2 p, vec2 a, vec2 b, vec2 c) {
  vec2 e0 = b - a, e1 = c - b, e2 = a - c, v0 = p - a, v1 = p - b, v2 = p - c;
  vec2 q0 = v0 - e0 * clamp(dot(v0, e0) / dot(e0, e0), 0.0, 1.0);
  vec2 q1 = v1 - e1 * clamp(dot(v1, e1) / dot(e1, e1), 0.0, 1.0);
  vec2 q2 = v2 - e2 * clamp(dot(v2, e2) / dot(e2, e2), 0.0, 1.0);
  float s = sign(e0.x * e2.y - e0.y * e2.x);
  vec2 d = min(min(vec2(dot(q0, q0), s * (v0.x * e0.y - v0.y * e0.x)), vec2(dot(q1, q1), s * (v1.x * e1.y - v1.y * e1.x))),
               vec2(dot(q2, q2), s * (v2.x * e2.y - v2.y * e2.x)));
  return -sqrt(d.x) * sign(d.y);
}
float ngon(vec2 p, float R, float n, float a0) {
  float an = 6.2831853 / n;
  float t = mod(atan(p.y, p.x) - a0, an) - 0.5 * an;
  return length(p) * cos(t) - R * cos(0.5 * an);
}
vec2 starVertex(int k, float R) {
  float a = -1.5707963 + float(k) * 0.62831853;
  return ((k & 1) == 1 ? 0.45 : 1.0) * R * vec2(cos(a), sin(a));
}
float star(vec2 p, float R) {
  float d = 1e9, s = 1.0;
  vec2 pj = starVertex(9, R);
  for (int k = 0; k < 10; k++) {
    vec2 pi = starVertex(k, R), e = pj - pi, w = p - pi;
    vec2 b = w - e * clamp(dot(w, e) / dot(e, e), 0.0, 1.0);
    d = min(d, dot(b, b));
    bvec3 c = bvec3(p.y >= pi.y, p.y < pj.y, e.x * w.y > e.y * w.x);
    if (all(c) || all(not(c))) s = -s;
    pj = pi;
  }
  return s * sqrt(d);
}
float shape(int id, vec2 p, float r) {
  if (id == 0) return length(p) - r;
  if (id == 1) return (abs(p.x) / (1.05 * r) + abs(p.y) / (1.3 * r) - 1.0) * 0.8 * r;
  if (id == 2) return box(p, vec2(0.85 * r));
  if (id == 3) return box(p, vec2(0.9 * r));
  if (id == 4) return tri(p, vec2(0.0, -1.1 * r), vec2(1.05 * r, 0.85 * r), vec2(-1.05 * r, 0.85 * r));
  if (id == 5) return tri(p, vec2(0.0, 1.1 * r), vec2(-1.05 * r, -0.85 * r), vec2(1.05 * r, -0.85 * r));
  if (id == 6) return min(box(p, vec2(r, 0.32 * r)), box(p, vec2(0.32 * r, r)));
  if (id == 7) return ngon(p, 1.1 * r, 6.0, 0.0);
  if (id == 8) return ngon(p, 1.1 * r, 5.0, -1.5707963);
  return star(p, 1.2 * r);
}
void main() {
  vec2 p = (gl_PointCoord - 0.5) * 2.0 * v_h;
  if (u_pass == 0) {
    float t = length(p) / v_h;
    if (t >= 1.0) discard;
    float a = t < 0.3 ? mix(0.667, 0.267, t / 0.3) : mix(0.267, 0.0, (t - 0.3) / 0.7);
    o = vec4(v_col, a * 0.55);
    return;
  }
  float d = shape(int(v_shape + 0.5), p, v_r);
  float lw = v_hot > 0.5 ? 1.8 : 1.3;
  float cover = clamp(0.5 - (v_out > 0.5 ? abs(d) - 0.5 * lw : d) * u_dpr, 0.0, 1.0);
  vec3 rgb = v_col;
  float a = cover * v_alpha;
  if (v_hot > 0.5) {
    float ring = clamp(0.5 - (abs(d) - 0.5) * u_dpr, 0.0, 1.0) * 0.9;
    rgb = mix(rgb, vec3(1.0), ring);
    a = max(a, ring);
  }
  if (a < 0.01) discard;
  o = vec4(rgb, a);
}`;

function compile(gl, type, src) {
  const s = gl.createShader(type);
  gl.shaderSource(s, src);
  gl.compileShader(s);
  if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s));
  return s;
}

function program(gl, vs, fs) {
  const p = gl.createProgram();
  gl.attachShader(p, compile(gl, gl.VERTEX_SHADER, vs));
  gl.attachShader(p, compile(gl, gl.FRAGMENT_SHADER, fs));
  gl.linkProgram(p);
  if (!gl.getProgramParameter(p, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(p));
  const u = {};
  for (let k = 0, n = gl.getProgramParameter(p, gl.ACTIVE_UNIFORMS); k < n; k++) {
    const name = gl.getActiveUniform(p, k).name.replace("[0]", "");
    u[name] = gl.getUniformLocation(p, gl.getActiveUniform(p, k).name);
  }
  return { p, u };
}

const hex = (c) => [1, 3, 5].map((i) => parseInt(c.slice(i, i + 2), 16) / 255);

// Returns null when WebGL2 is not available; the caller then keeps drawing
// with the 2D canvas.
function createGL(canvas) {
  let gl;
  try { gl = canvas.getContext("webgl2", { alpha: false, antialias: false, powerPreference: "high-performance" }); } catch { gl = null; }
  if (!gl) return null;
  let links, nodes;
  try {
    links = program(gl, LINK_VS, LINK_FS);
    nodes = program(gl, NODE_VS, NODE_FS);
  } catch (e) { console.error("webgl shaders:", e); return null; }

  const maxPt = Math.min(256, gl.getParameter(gl.ALIASED_POINT_SIZE_RANGE)[1]);
  const typeNames = Object.keys(TYPES);
  const ty = new Float32Array(12 * 4), tmin = new Float32Array(12);
  typeNames.forEach((name, k) => {
    const t = TYPES[name];
    ty.set([...hex(t.color), SHAPES.indexOf(t.shape)], k * 4);
    tmin[k] = t.min;
  });
  const grp = new Float32Array(5 * 4);
  GROUPS.forEach((g, k) => {
    const [c, a] = GROUP_STYLE[g];
    grp.set([...c.match(/\d+/g).map((x) => x / 255), a], k * 4);
  });

  const mkTex = () => {
    const t = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, t);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    return t;
  };
  const upload = (tex, unit, data, h) => {
    gl.activeTexture(gl.TEXTURE0 + unit);
    gl.bindTexture(gl.TEXTURE_2D, tex);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA32F, TEX_W, h, 0, gl.RGBA, gl.FLOAT, data);
  };
  const posTex = mkTex(), stTex = mkTex();
  const linkVao = gl.createVertexArray(), nodeVao = gl.createVertexArray();
  let linkBuf = null, metaBuf = null, nLinks = 0, nNodes = 0, texH = 1;

  const api = {
    // Static graph data: positions, node type/size and link endpoints.
    load(S) {
      nNodes = S.nodes.length;
      nLinks = S.links.length;
      texH = Math.ceil(nNodes / TEX_W);
      const pos = new Float32Array(TEX_W * texH * 4), meta = new Float32Array(nNodes * 4);
      S.nodes.forEach((n, i) => {
        pos.set([n.x, n.y, n.z, 0], i * 4);
        meta.set([typeNames.indexOf(n.type), n.r, isOutlined(n) ? 1 : 0, n.deg > 25 ? 1 : 0], i * 4);
      });
      upload(posTex, 0, pos, texH);
      const lk = new Int32Array(nLinks * 6), gid = (l) => Math.max(0, GROUPS.indexOf(l.g));
      S.links.forEach((l, k) => lk.set([l.s, l.t, gid(l), l.t, l.s, gid(l)], k * 6));
      if (linkBuf) gl.deleteBuffer(linkBuf);
      if (metaBuf) gl.deleteBuffer(metaBuf);
      linkBuf = gl.createBuffer();
      gl.bindVertexArray(linkVao);
      gl.bindBuffer(gl.ARRAY_BUFFER, linkBuf);
      gl.bufferData(gl.ARRAY_BUFFER, lk, gl.STATIC_DRAW);
      const la = gl.getAttribLocation(links.p, "a_link");
      gl.enableVertexAttribArray(la);
      gl.vertexAttribIPointer(la, 3, gl.INT, 0, 0);
      metaBuf = gl.createBuffer();
      gl.bindVertexArray(nodeVao);
      gl.bindBuffer(gl.ARRAY_BUFFER, metaBuf);
      gl.bufferData(gl.ARRAY_BUFFER, meta, gl.STATIC_DRAW);
      const na = gl.getAttribLocation(nodes.p, "a_meta");
      gl.enableVertexAttribArray(na);
      gl.vertexAttribPointer(na, 4, gl.FLOAT, false, 0, 0);
      gl.bindVertexArray(null);
    },

    // Per-node state: visible, in tab, search match (0 none, 1 match, 2 miss),
    // selection (0 none, 1 neighbour, 2 selected).
    state(S) {
      const st = new Float32Array(TEX_W * texH * 4);
      for (let i = 0; i < nNodes; i++) {
        const n = S.nodes[i];
        st[i * 4] = S.vis[i] && !n.hid ? 1 : 0;
        st[i * 4 + 1] = inTab(n) ? 1 : 0;
        st[i * 4 + 2] = S.matches ? (S.matches.has(i) ? 1 : 2) : 0;
        st[i * 4 + 3] = i === S.sel ? 2 : S.sel >= 0 && S.neigh.has(i) ? 1 : 0;
      }
      upload(stTex, 1, st, texH);
    },

    draw(S, w, h, dpr) {
      const cw = Math.round(w * dpr), ch = Math.round(h * dpr);
      if (canvas.width !== cw || canvas.height !== ch) { canvas.width = cw; canvas.height = ch; }
      gl.viewport(0, 0, cw, ch);
      gl.clearColor(0.0157, 0.0275, 0.0431, 1);
      gl.clear(gl.COLOR_BUFFER_BIT);
      gl.enable(gl.BLEND);
      gl.activeTexture(gl.TEXTURE0); gl.bindTexture(gl.TEXTURE_2D, posTex);
      gl.activeTexture(gl.TEXTURE1); gl.bindTexture(gl.TEXTURE_2D, stTex);
      const cam = [Math.cos(S.yaw), Math.sin(S.yaw), Math.cos(S.pitch), Math.sin(S.pitch)];
      const common = (pr) => {
        gl.useProgram(pr.p);
        gl.uniform1i(pr.u.u_pos, 0); gl.uniform1i(pr.u.u_st, 1);
        gl.uniform3fv(pr.u.u_target, S.target); gl.uniform4fv(pr.u.u_cam, cam);
        gl.uniform1f(pr.u.u_dist, S.dist); gl.uniform1f(pr.u.u_f, Math.min(w, h) * 1.15);
        gl.uniform2f(pr.u.u_res, w, h);
        gl.uniform1i(pr.u.u_sel, S.sel); gl.uniform1i(pr.u.u_hasSel, S.sel >= 0 ? 1 : 0);
      };

      gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);
      common(links);
      gl.uniform1i(links.u.u_query, S.matches ? 1 : 0);
      gl.uniform1f(links.u.u_dim, S.matches || S.sel >= 0 ? 0.45 : 1);
      gl.uniform4fv(links.u.u_grp, grp);
      gl.bindVertexArray(linkVao);
      gl.uniform1i(links.u.u_pass, 0);
      gl.drawArrays(gl.LINES, 0, nLinks * 2);
      if (S.sel >= 0) { gl.uniform1i(links.u.u_pass, 1); gl.drawArrays(gl.LINES, 0, nLinks * 2); }

      common(nodes);
      gl.uniform1i(nodes.u.u_hover, S.hover); gl.uniform1f(nodes.u.u_dpr, dpr); gl.uniform1f(nodes.u.u_maxPt, maxPt);
      gl.uniform4fv(nodes.u.u_ty, ty); gl.uniform1fv(nodes.u.u_min, tmin);
      gl.bindVertexArray(nodeVao);
      gl.blendFunc(gl.SRC_ALPHA, gl.ONE);
      gl.uniform1i(nodes.u.u_pass, 0);
      gl.drawArrays(gl.POINTS, 0, nNodes);
      gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);
      gl.uniform1i(nodes.u.u_pass, 1);
      gl.drawArrays(gl.POINTS, 0, nNodes);
      gl.bindVertexArray(null);
    },
  };
  return api;
}
