// The map's data, shared state and constants, and the helpers every view
// uses: geometry, drawing primitives, names and what relates to the
// selection.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

let D = /*DATA*/null;
// With no data embedded, the page is the live map: it loads the view from the
// server that served it and redraws on every rebuild.
const LIVE = D === null;
let modules, symbols, byModule, edges, links, removedLinks, groups, groupOf, parentOf, members;
// ingest indexes one view. Everything drawn is derived from D and these maps.
function ingest(data) {
  D = data;
  for (const key of ["modules", "symbols", "edges", "links", "findings", "designs", "problems"]) D[key] = D[key] || [];
  modules = new Map(D.modules.map(m => [m.id, m]));
  symbols = new Map(D.symbols.map(s => [s.id, s]));
  byModule = new Map();
  for (const s of D.symbols) { if (!byModule.has(s.module)) byModule.set(s.module, []); byModule.get(s.module).push(s); }
  // An edge made only of imports, such as a blank import, has no references.
  for (const e of D.edges) { e.references = e.references || []; e.imports = e.imports || []; }
  edges = D.edges; links = D.links; removedLinks = D.removed_links || [];
  const note = document.getElementById("legend-public");
  if (note) note.hidden = D.architecture !== "modular";
  groups = new Map((D.layout.groups || []).map(g => [g.id, g]));
  groupOf = new Map();
  for (const g of D.layout.groups || []) for (const id of g.modules) if (!groupOf.has(id) || groups.get(groupOf.get(id)).depth < g.depth) groupOf.set(id, g.id);
  // A member such as Service.Map hangs under the type it belongs to in the
  // symbols view.
  parentOf = new Map(); members = new Map();
  for (const s of D.symbols) {
    const at = s.name.lastIndexOf("."); if (at < 0) continue;
    const owner = (byModule.get(s.module) || []).find(p => p.name === s.name.slice(0, at)); if (!owner) continue;
    parentOf.set(s.id, owner);
    if (!members.has(owner.id)) members.set(owner.id, []);
    members.get(owner.id).push(s);
  }
  placeMoved();
  layoutFiles();
  // The caller draws next, so the theme only sets the tokens here.
  fillThemeMenu(); applyTheme(false);
}
const NS = "http://www.w3.org/2000/svg";
const ICON_GROWTH = 0.4;
const inherits = kind => kind === "implements" || kind === "embeds";
const HIT_SLOP = 12;
// A press held this long without moving picks a module up.
const LONG_PRESS_MS = 450;
// The architectures whose layers treaty.yaml declares by glob, so a module
// can move between them on the map.
const LAYERED_STYLES = ["hexagonal", "clean", "layered"];
// Room kept between a moved module and the edge of the group around it.
const GROUP_ROOM = 6;
const AUTO_COLLAPSE_PX = 45;
// A file cell shows its symbols once it is this many pixels across; smaller,
// the symbols stay on the module's border and the cells show only shading.
const CELL_DETAIL_PX = 20;
const LABEL_MIN_PX = 9;
// mapTab is the map view shown: references, files or symbols. Each view
// keeps its own camera in views while another is shown, and trail is the
// path open in the symbols view.
const state = { pointer: null, selected: null, internals: false, symbolPos: new Map(), view: null, groupOpen: {}, signature: undefined, rendering: false, positions: {}, moved: new Set(), mapTab: "references", views: {}, trail: [], treeFocus: null };
const home = () => { const E = (state.mapTab === "files" && filesLayout ? filesLayout.extent : D.layout.extent) + 20; return { x: -E, y: -E, w: 2 * E, h: 2 * E }; };
function applyView() {
  if (!D) return;
  if (!state.view) state.view = home();
  const v = state.view, svg = document.getElementById("map");
  svg.setAttribute("viewBox", `${v.x} ${v.y} ${v.w} ${v.h}`);
  // k < 1 when zoomed in. Anchored icons grow on screen by zoom^ICON_GROWTH,
  // so they neither crowd each other nor vanish as the map grows.
  const k = v.w / home().w, partial = Math.pow(k, 1 - ICON_GROWTH);
  // Labels are 10 map units tall. Boost them so they stay readable at the
  // fitted view however large the map is; icons stay tied to the geometry.
  const rect = svg.getBoundingClientRect(), fitted = Math.min(rect.width / home().w, rect.height / home().h) || 1;
  const boost = Math.max(1, LABEL_MIN_PX / (10 * fitted));
  for (const node of svg.querySelectorAll("[data-ax]")) {
    const scale = (node.dataset.cap ? Math.min(partial, 1) : partial) * (node.dataset.text ? boost : 1);
    node.setAttribute("transform", `translate(${node.dataset.ax} ${node.dataset.ay}) scale(${scale})`);
  }

  sizeGlows();
  if (!state.rendering && state.signature !== undefined && typeof collapsedSignature === "function" && collapsedSignature() !== state.signature) render();
}
// sizeGlows gives each glowing element --u, one screen pixel in its own
// units, so its glow is the same size on screen at any zoom.
function sizeGlows() {
  for (const node of document.querySelectorAll("#map .selected-glow, #map .pointed-glow, #map .glow")) {
    const m = node.getScreenCTM && node.getScreenCTM();
    if (m) node.style.setProperty("--u", (1 / (Math.hypot(m.a, m.b) || 1)).toFixed(4));
  }
}
// anchored draws children around (0, 0) and scales them by only part of the
// zoom (see ICON_GROWTH); cap lets them shrink fully when zoomed out.
function anchored(parent, x, y, attrs = {}, cap = false, text = false) {
  const g = el("g", { ...attrs, "data-ax": x, "data-ay": y }, parent);
  if (cap) g.dataset.cap = "1";
  if (text) g.dataset.text = "1";
  return g;
}
function toMap(clientX, clientY) {
  const svg = document.getElementById("map"), point = svg.createSVGPoint(); point.x = clientX; point.y = clientY;
  return point.matrixTransform(svg.getScreenCTM().inverse());
}
function zoom(factor, center) {
  if (!D) return;
  const v = state.view, limit = home().w;
  const w = Math.min(limit * 3, Math.max(limit * 0.05, v.w * factor)), scale = w / v.w;
  const c = center || { x: v.x + v.w / 2, y: v.y + v.h / 2 };
  state.view = { x: c.x - (c.x - v.x) * scale, y: c.y - (c.y - v.y) * scale, w, h: v.h * scale };
  applyView();
}

function el(tag, attrs = {}, parent) {
  const node = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  if (parent) parent.appendChild(node);
  return node;
}
function h(tag, attrs = {}, text) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  if (text !== undefined) node.textContent = text;
  return node;
}
function hexPoints(cx, cy, r) {
  const pts = [];
  for (let i = 0; i < 6; i++) { const a = Math.PI / 3 * i; pts.push([cx + r * Math.cos(a), cy + r * Math.sin(a)]); }
  return pts;
}
// onBorder finds where a ray from a flat-top hexagon's center at an angle
// meets its border, scaled by inset.
function onBorder(cx, cy, r, angle, inset = 1) {
  const normal = Math.PI / 6 + Math.PI / 3 * Math.round((angle - Math.PI / 6) / (Math.PI / 3));
  const d = inset * r * Math.cos(Math.PI / 6) / Math.cos(angle - normal);
  return [cx + d * Math.cos(angle), cy + d * Math.sin(angle)];
}
function onPerimeter(cx, cy, r, t) {
  const pts = hexPoints(cx, cy, r); const seg = t * 6; const i = Math.floor(seg) % 6; const f = seg - Math.floor(seg);
  const a = pts[i], b = pts[(i + 1) % 6];
  return [a[0] + (b[0] - a[0]) * f, a[1] + (b[1] - a[1]) * f];
}
// symbolOrder orders symbols within a file, on the border and inside its
// cell: values, types, interfaces, methods, then functions, each by name.
const KIND_RANK = { value: 0, type: 1, interface: 2, method: 3, function: 4 };
function symbolOrder(a, b) {
  const ka = KIND_RANK[a.kind] ?? 5, kb = KIND_RANK[b.kind] ?? 5;
  return ka - kb || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0);
}
function shortName(path) { const parts = path.split("/"); return parts.slice(-2).join("/"); }
function color(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }
function glyph(change) { return { added: "+", contract: "Δ", breaking: "!", removed: "−", implementation: "~", moved: "→" }[change] || ""; }

// drawShape draws one symbol icon. Shape and fill show the kind: hollow
// circle function, filled circle method, hollow square interface, filled
// square concrete type, filled triangle value. Color shows the change (grey
// unchanged, green added, red removed, amber changed), with a glyph beside
// the icon.
// Designed or planned symbols that are not built yet are teal with a dashed
// outline, so the state never relies on color alone.
function drawShape(g, kind, x, y, r, change, design) {
  const hue = change === "added" ? color("--added") : change === "removed" ? color("--removed") : change ? color("--changed") : color("--module-stroke");
  const hollow = kind === "function" || kind === "interface";
  const common = { fill: hollow ? color("--module") : hue, stroke: hue, "stroke-width": hollow ? 1.6 : 1 };
  if (design) Object.assign(common, { fill: color("--module"), stroke: color("--planned"), "stroke-width": 1.6, "stroke-dasharray": "2 1.5" });
  let shape;
  if (kind === "interface" || kind === "type") shape = el("rect", { ...common, x: x - r * 0.85, y: y - r * 0.85, width: r * 1.7, height: r * 1.7 }, g);
  else if (kind === "value") shape = el("polygon", { ...common, points: `${x},${y - r} ${x + r},${y + r * 0.8} ${x - r},${y + r * 0.8}` }, g);
  else shape = el("circle", { ...common, cx: x, cy: y, r }, g);
  const text = glyph(change);
  if (text) el("text", { x: x + r * 1.1, y: y - r * 0.6, "font-size": r * 1.5, "font-weight": 700, fill: hue, "pointer-events": "none" }, g).textContent = text;
  return shape;
}

// Groups are directories drawn as hexagons around their packages. They are
// visual only. A group collapses automatically when it is small on screen,
// and state.groupOpen overrides that per group (double-click, or Go to on
// something inside it). The layout never moves: collapsing only redraws.
function ancestors(groupId) { const result = []; for (let g = groups.get(groupId); g; g = groups.get(g.parent)) result.unshift(g); return result; }
function pixelsPerUnit() { const rect = document.getElementById("map").getBoundingClientRect(); const v = state.view || home(); return Math.min(rect.width / v.w, rect.height / v.h) || 1; }
function isCollapsed(g) {
  if (g.id in state.groupOpen) return !state.groupOpen[g.id];
  return g.radius * pixelsPerUnit() < AUTO_COLLAPSE_PX;
}
// anchorOf is where a module shows on the map: itself, or the outermost
// collapsed group that holds it.
function anchorOf(moduleId) {
  for (const g of ancestors(groupOf.get(moduleId))) if (isCollapsed(g)) return { key: g.id, x: g.x, y: g.y, r: g.radius * Math.cos(Math.PI / 6), label: g.path + "/", layer: g.layer, group: g };
  const p = D.layout.modules[moduleId], m = modules.get(moduleId);
  return p ? { key: moduleId, x: p.x, y: p.y, r: moduleSize(moduleId), label: m.label || m.path, layer: m.layer } : null;
}
function cellDetail() { return (D.layout.cell_size || 0) * pixelsPerUnit() >= CELL_DETAIL_PX; }
function collapsedSignature() {
  // The files view redraws every half doubling of zoom, to show or hide names
  // and symbols.
  if (state.mapTab === "files") return "files|" + Math.round(Math.log2(pixelsPerUnit()) * 2);
  return (D.layout.groups || []).filter(isCollapsed).map(g => g.id).join("|") + (cellDetail() ? "|cells" : "");
}
// placeName says where the architecture puts a module: its slice or
// context, its layer and side, and whether it is public.
function placeName(m) {
  const within = m.section ? m.section.split("/").pop() + " · " : "";
  return within + m.layer + (m.side ? ` (${m.side})` : "") + (m.public ? " (public)" : "");
}
// moduleSize is a module hexagon's radius: larger for a module with more
// files, since each file is a cell inside it.
function fileKey(moduleId, file) { return moduleId + "|" + file; }
// fileOf splits a file selection's id into its module and path.
// fileSource is a file's text from the payload, or empty when the payload
// left it out.
function fileSource(file) { return (D.sources || {})[file] || ""; }
// codeOf cuts a symbol's lines from its file, as long as they are few enough
// to read in the inspector.
function codeOf(s) {
  const text = s.file && fileSource(s.file); if (!text || !s.line) return "";
  const end = Math.max(s.end_line || s.line, s.line); if (end - s.line > 200) return "";
  return text.split("\n").slice(s.line - 1, end).join("\n");
}
function fileOf(key) { const at = key.indexOf("|"); return { module: key.slice(0, at), file: key.slice(at + 1) }; }
function fileCell(key) { const f = fileOf(key); return ((D.layout.cells || {})[f.module] || []).find(c => c.file === f.file); }
function moduleSize(moduleId) { return (D.layout.sizes || {})[moduleId] || D.layout.size; }
// moduleLabel names a module relative to the group drawn around it, so a
// group's children don't all repeat its path.
function moduleLabel(m) {
  const g = groups.get(groupOf.get(m.id));
  if (g && m.path.startsWith(g.path + "/")) return m.path.slice(g.path.length + 1);
  if (g && m.path === g.path) return m.path.split("/").pop();
  return m.path === "." ? m.label || m.path : shortName(m.path);
}
function openAncestors(moduleId) { for (const g of ancestors(groupOf.get(moduleId))) state.groupOpen[g.id] = true; }

function related() {
  const sel = state.selected; if (!sel) return null;
  const set = new Set();
  if (sel.type === "symbol") {
    set.add(sel.id); const s = symbols.get(sel.id); if (s) set.add(s.module);
    for (const l of links) if (l.from === sel.id || l.to === sel.id) { set.add(l.from); set.add(l.to); const a = symbols.get(l.from), b = symbols.get(l.to); if (a) set.add(a.module); if (b) set.add(b.module); }
  } else if (sel.type === "module") {
    set.add(sel.id); for (const s of byModule.get(sel.id) || []) set.add(s.id);
    for (const e of edges) if (e.from === sel.id || e.to === sel.id) { set.add(e.from); set.add(e.to); }
  } else if (sel.type === "file") {
    const f = fileOf(sel.id); set.add(sel.id); set.add(f.module);
    const own = new Set((byModule.get(f.module) || []).filter(s => s.file === f.file).map(s => s.id));
    for (const id of own) set.add(id);
    for (const l of links) if (own.has(l.from) || own.has(l.to)) { set.add(l.from); set.add(l.to); const a = symbols.get(l.from), b = symbols.get(l.to); if (a) set.add(a.module); if (b) set.add(b.module); }
  } else if (sel.type === "group") {
    const g = groups.get(sel.id); set.add(sel.id);
    for (const id of g.modules) { set.add(id); for (const s of byModule.get(id) || []) set.add(s.id); }
    for (const e of edges) if (g.modules.includes(e.from) || g.modules.includes(e.to)) { set.add(e.from); set.add(e.to); }
  } else if (sel.type === "edge") { for (const r of sel.edge.references) { const a = symbols.get(r.from), b = symbols.get(r.to); set.add(r.from); set.add(r.to); if (a) set.add(a.module); if (b) set.add(b.module); } }
  return set;
}
function relatedKey(rel, key) { if (!rel) return true; const g = groups.get(key); return g ? rel.has(key) || g.modules.some(id => rel.has(id)) : rel.has(key); }
