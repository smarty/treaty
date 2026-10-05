// The references view: rings or regions, groups, modules with their file
// cells and contracts, and the dependencies between them.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

function render() {
  if (!D) return;
  if (state.mapTab === "symbols") { renderSymbolTree(); return; }
  state.rendering = true;
  try { draw(); } finally { state.rendering = false; }
  if (carry) liftCarried();
}
function draw() {
  const svg = document.getElementById("map"); svg.innerHTML = "";
  applyView();
  state.signature = collapsedSignature();
  state.symbolPos = new Map();
  const rel = related();
  if (state.mapTab === "files") drawFiles(svg, rel); else drawReferences(svg, rel);
  if (state.selected && state.selected.type === "symbol") {
    const layer = el("g", {}, svg);
    const own = state.symbolPos.get(state.selected.id), s = symbols.get(state.selected.id);
    if (own && s) el("text", { x: 8, y: -8, "font-size": 11, class: "selected-label" }, anchored(layer, own[0], own[1], {}, false, true)).textContent = s.name;
    const where = id => { const at = positionOf({ type: "symbol", id }); return at ? [at.x, at.y] : null; };
    // References the base had and the working tree lost draw red and dashed.
    for (const l of [...links, ...removedLinks.map(r => ({ ...r, removed: true }))]) {
      if (l.from !== state.selected.id && l.to !== state.selected.id) continue;
      const a = where(l.from), b = where(l.to); if (!a || !b) continue;
      const line = el("line", { x1: a[0], y1: a[1], x2: b[0], y2: b[1], stroke: color(l.removed ? "--removed" : "--accent"), "stroke-width": 1.5, "vector-effect": "non-scaling-stroke" }, layer);
      if (inherits(l.kind) || l.removed) line.setAttribute("stroke-dasharray", l.removed ? "2 3" : "5 3");
      const other = l.from === state.selected.id ? l.to : l.from, o = where(other);
      el("text", { x: 6, y: -6, "font-size": 10, fill: color("--ink") }, anchored(layer, o[0], o[1], {}, false, true)).textContent = (symbols.get(other) || {}).name || other;
    }
  }
  const picked = state.selected && state.selected.type === "symbol" && symbols.get(state.selected.id);
  const pickedFile = state.selected && state.selected.type === "file" ? state.selected.id : picked && picked.file ? fileKey(picked.module, picked.file) : "";
  lightFile(pickedFile, "file-picked");
  state.hotFile = "";
  const selectedNode = state.selected && (state.selected.type === "edge" ? svg.querySelector(`[data-edge="${CSS.escape(state.selected.from + "|" + state.selected.to + (state.selected.removed ? "|removed" : ""))}"]`) : glowNode(state.selected.id));
  if (selectedNode) selectedNode.classList.add("selected-glow");
  const pointedNode = state.pointer && Date.now() < state.pointer.until && glowNode(state.pointer.target);
  if (pointedNode) pointedNode.classList.add("pointed-glow");
  applyView();
}
// drawReferences draws the references view: the architecture's rings or
// areas, the groups, the modules with their files and contracts, and the
// dependencies between them.
function drawReferences(svg, rel) {
  // Rings for hexagonal and clean; bands, slices, cells and contexts as
  // regions for the other architectures.
  const tones = ["--ring-domain", "--ring-app", "--ring-adapter", "--ring-outer"];
  for (const ring of [...(D.layout.rings || [])].reverse()) {
    el("polygon", { class: "backdrop", "data-band": "ring:" + ring.layer, points: hexPoints(0, 0, ring.radius).map(p => p.join(",")).join(" "), fill: color(tones[ring.tone] || tones[3]), stroke: color("--ring-stroke") }, svg);
    el("text", { x: 0, y: 0, "text-anchor": "middle", class: "ring-label" }, anchored(svg, 0, -ring.radius * Math.sin(Math.PI / 3) + 16, {}, true, true)).textContent = ring.layer.replace(/_/g, " ");
  }
  for (const [index, r] of (D.layout.regions || []).entries()) {
    el("rect", { class: "backdrop", "data-band": "region:" + index, x: r.x, y: r.y, width: r.w, height: r.h, rx: 8, fill: color(tones[r.tone] || tones[3]), stroke: color("--ring-stroke"), "vector-effect": "non-scaling-stroke" }, svg);
    el("text", { x: 0, y: 0, class: "ring-label" }, anchored(svg, r.x + 10, r.y + 17, {}, true, true)).textContent = r.label;
  }
  for (const l of D.layout.labels || []) el("text", { x: 0, y: 0, "text-anchor": "middle", class: "ring-label" }, anchored(svg, l.x, l.y, {}, true, true)).textContent = l.text;

  // Groups, outermost first. A collapsed group hides everything inside it.
  const hidden = new Set();
  const groupLayer = el("g", {}, svg);
  for (const g of D.layout.groups || []) {
    if (ancestors(g.parent).some(isCollapsed)) continue;
    const collapsed = isCollapsed(g);
    if (collapsed) for (const id of g.modules) hidden.add(id);
    const selected = state.selected && state.selected.type === "group" && state.selected.id === g.id;
    const count = g.modules.length, contracts = g.modules.reduce((n, id) => n + (byModule.get(id) || []).filter(s => s.contract).length, 0);
    const parentPath = (groups.get(g.parent) || {}).path;
    const name = parentPath ? g.path.slice(parentPath.length + 1) : g.path;
    const node = el("g", { class: "group" + (relatedKey(rel, g.id) ? "" : " dim"), "data-group": g.id, tabindex: 0, role: "button", "aria-label": `Group ${g.path}, ${count} modules${collapsed ? ", collapsed" : ""}`, "data-label": `${g.path}/ · ${count} module${count === 1 ? "" : "s"} · ${contracts} contracts · ${collapsed ? "double-click to expand" : "double-click to collapse"}` }, groupLayer);
    el("polygon", { points: hexPoints(g.x, g.y, g.radius).map(q => q.join(",")).join(" "), fill: color(collapsed ? "--group-collapsed" : "--group"), stroke: selected ? color("--selection") : color("--hex-stroke"), "stroke-width": selected ? 2.5 : 1, "vector-effect": "non-scaling-stroke" }, node);
    const top = g.y - g.radius * Math.cos(Math.PI / 6);
    if (collapsed) {
      el("text", { x: 0, y: 0, "text-anchor": "middle", class: "group-name" + (selected ? " selected-label" : "") }, anchored(node, g.x, g.y - 4, {}, true, true)).textContent = name + "/";
      el("text", { x: 0, y: 0, "text-anchor": "middle", class: "group-count" }, anchored(node, g.x, g.y + 12, {}, true, true)).textContent = `${count} modules · ${contracts} contracts`;
    } else {
      el("text", { x: 0, y: 0, "text-anchor": "middle", class: "group-name" + (selected ? " selected-label" : "") }, anchored(node, g.x, top + 13, {}, true, true)).textContent = name + "/";
    }
    const pick = () => select({ type: "group", id: g.id });
    node.addEventListener("click", pick);
    node.addEventListener("dblclick", ev => { ev.stopPropagation(); toggleGroup(g.id); });
    node.addEventListener("keydown", ev => { if (ev_is(ev)) { ev.preventDefault(); pick(); } });
  }

  // Edges run between anchors: modules, or the collapsed groups hiding them.
  const pairs = new Map();
  for (const e of edges) {
    const a = anchorOf(e.from), b = anchorOf(e.to); if (!a || !b || a.key === b.key) continue;
    // A removed dependency stays apart from a live one between the same ends.
    const key = a.key + "\u0000" + b.key + (e.removed ? "\u0000removed" : "");
    if (!pairs.has(key)) pairs.set(key, { from: a.key, to: b.key, a, b, references: [], imports: [], count: 0, violation: false, new: false, changed: false, removed: !!e.removed });
    const pair = pairs.get(key); pair.references.push(...e.references); pair.imports.push(...(e.imports || [])); pair.count += e.count; pair.violation ||= e.violation; pair.new ||= e.new; pair.changed ||= e.changed;
  }
  const edgeLayer = el("g", {}, svg);
  const maxCount = Math.max(1, ...[...pairs.values()].map(e => e.count));
  for (const e of pairs.values()) {
    const from = e.a, to = e.b;
    // Uses (calls, type uses) draw solid; inherits (implements, embeds) draw
    // dashed with a hollow head. A pair with both bows them to opposite sides.
    const uses = e.references.filter(r => !inherits(r.kind)), inherited = e.references.filter(r => inherits(r.kind));
    const parts = [];
    // An edge with only imports, such as a blank import, still draws as a use.
    if (uses.length || (!inherited.length && e.imports.length)) parts.push({ refs: uses, inherit: false, bend: 0.12 });
    if (inherited.length) parts.push({ refs: inherited, inherit: true, bend: uses.length ? -0.12 : 0.12 });
    parts.forEach((part, index) => {
      const mx = (from.x + to.x) / 2, my = (from.y + to.y) / 2, dx = to.x - from.x, dy = to.y - from.y;
      const cx = mx - dy * part.bend, cy = my + dx * part.bend;
      const trim = (p, toward, by) => { const vx = toward[0] - p.x, vy = toward[1] - p.y, d = Math.hypot(vx, vy) || 1; return { x: p.x + vx / d * by, y: p.y + vy / d * by }; };
      const a = trim(from, [cx, cy], from.r + 4), b = trim(to, [cx, cy], to.r + 8);
      const stroke = e.violation ? color("--violation") : e.new ? color("--added") : e.removed ? color("--removed") : e.changed ? color("--changed") : color("--edge");
      const width = e.violation ? 2.5 : 1 + 3 * part.refs.length / maxCount;
      const dimmed = rel && !(relatedKey(rel, e.from) && relatedKey(rel, e.to));
      const kinds = part.inherit ? [...new Set(part.refs.map(r => r.kind))].join(", ") : "uses";
      const imported = !part.inherit && !part.refs.length ? ` · ${e.imports.length} import${e.imports.length === 1 ? "" : "s"}, no references` : "";
      const label = `${from.label} → ${to.label} · ${kinds} · ${part.refs.length} reference${part.refs.length === 1 ? "" : "s"}${imported}${e.violation ? " · violation" + (e.rule ? ": " + e.rule : "") : ""}${e.removed ? " · removed" : e.new ? " · added" : e.changed ? " · changed" : ""}`;
      const path = el("path", { d: `M${a.x},${a.y} Q${cx},${cy} ${b.x},${b.y}`, fill: "none", stroke, "stroke-width": width, "marker-end": part.inherit ? "url(#inherit)" : "url(#arrow)", "vector-effect": "non-scaling-stroke", "data-label": label, class: dimmed && !e.violation ? "dim" : "", tabindex: 0, role: "button", "aria-label": `${part.inherit ? "Inherits" : "Dependency"} ${from.label} to ${to.label}${e.violation ? ", layer violation" : ""}` }, edgeLayer);
      if (part.inherit || e.removed) path.setAttribute("stroke-dasharray", e.removed ? "3 3" : "6 4");
      path.dataset.edge = e.from + "|" + e.to + (e.removed ? "|removed" : "");
      const pick = () => select({ type: "edge", from: e.from, to: e.to, removed: e.removed, edge: e });
      const hit = el("path", { d: path.getAttribute("d"), fill: "none", stroke: "transparent", "stroke-width": 14, "vector-effect": "non-scaling-stroke", "pointer-events": "stroke", "data-label": label }, edgeLayer);
      hit.glowTarget = path;
      for (const target of [path, hit]) { target.style.cursor = "pointer"; target.addEventListener("click", pick); }
      path.addEventListener("keydown", ev => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); pick(); } });
      if (e.violation && index === 0) {
        const qx = 0.25 * a.x + 0.5 * cx + 0.25 * b.x, qy = 0.25 * a.y + 0.5 * cy + 0.25 * b.y;
        const mark = anchored(edgeLayer, qx, qy, { "data-label": `Layer violation: ${from.label} → ${to.label}` });
        el("circle", { cx: 0, cy: 0, r: 8, fill: color("--violation") }, mark);
        el("text", { x: 0, y: 4, "text-anchor": "middle", "font-size": 11, "font-weight": 700, fill: "#fff", "pointer-events": "none" }, mark).textContent = "!";
      }
    });
  }
  const defs = el("defs", {}, svg);
  const marker = el("marker", { id: "arrow", viewBox: "0 0 10 10", refX: 9, refY: 5, markerWidth: 9, markerHeight: 9, markerUnits: "userSpaceOnUse", orient: "auto" }, defs);
  el("path", { d: "M0,0 L10,5 L0,10 z", fill: color("--arrow") }, marker);
  const hollow = el("marker", { id: "inherit", viewBox: "-1 -1 12 12", refX: 10, refY: 5, markerWidth: 13, markerHeight: 13, markerUnits: "userSpaceOnUse", orient: "auto" }, defs);
  el("path", { d: "M0,0 L10,5 L0,10 z", fill: color("--bg"), stroke: color("--arrow"), "stroke-width": 1.2 }, hollow);

  const pos = D.layout.modules;
  const cellsOf = D.layout.cells || {};
  // A file's shade grows with its share of the most crowded file's symbols.
  const crowded = Math.max(1, ...Object.values(cellsOf).flat().map(c => c.symbols));
  for (const m of D.modules) {
    const p = pos[m.id]; if (!p || hidden.has(m.id)) continue;
    const S = moduleSize(m.id);
    const g = el("g", { class: "module" + (rel && !rel.has(m.id) ? " dim" : ""), "data-module": m.id, tabindex: 0, role: "button", "aria-label": `Module ${m.id}, ${m.layer}`, "data-label": `${m.label || m.path} · ${placeName(m)}${m.design ? " · planned, not built" : ""}${m.removed ? " · removed" : ""}` }, svg);
    const hex = el("polygon", { points: hexPoints(p.x, p.y, S).map(q => q.join(",")).join(" "), fill: color("--module"), stroke: m.new ? color("--added") : m.removed ? color("--removed") : m.design ? color("--planned") : state.selected && state.selected.id === m.id ? color("--selection") : color("--hex-stroke"), "stroke-width": state.selected && state.selected.id === m.id ? 2.5 : 1.4, "vector-effect": "non-scaling-stroke" }, g);
    if (m.new || m.removed || m.design) hex.setAttribute("stroke-dasharray", "5 3");
    // Composition wires adapters into the core: a double outline marks the
    // only modules allowed to depend on adapters.
    if (m.layer === "composition") el("polygon", { points: hexPoints(p.x, p.y, S * 0.84).map(q => q.join(",")).join(" "), fill: "none", stroke: color("--hex-stroke"), "stroke-width": 1, "vector-effect": "non-scaling-stroke", "pointer-events": "none" }, g);
    // A context's public API is the only part other contexts may use: a
    // bold outline marks it.
    if (m.public) el("polygon", { points: hexPoints(p.x, p.y, S * 1.12).map(q => q.join(",")).join(" "), fill: "none", stroke: color("--module-stroke"), "stroke-width": 2.4, "vector-effect": "non-scaling-stroke", "pointer-events": "none" }, g);
    el("text", { x: 0, y: 0, "text-anchor": "middle", class: "module-label" + (state.selected && state.selected.type === "module" && state.selected.id === m.id ? " selected-label" : "") }, anchored(g, p.x, p.y + S + 13, {}, true, true)).textContent = moduleLabel(m);
    const pick = () => select({ type: "module", id: m.id });
    g.addEventListener("click", ev => {
      ev.stopPropagation();
      if (ev.target.dataset && ev.target.dataset.file && !ev.target.closest("[data-symbol]")) select({ type: "file", id: ev.target.dataset.file });
      else if (ev.target === hex || ev.target.tagName === "text") pick();
    });
    g.addEventListener("keydown", ev => {
      if (!ev_is(ev)) return;
      ev.preventDefault();
      if (ev.target.classList.contains("file-cell")) select({ type: "file", id: ev.target.dataset.file }); else pick();
    });
    const all = byModule.get(m.id) || [];
    const contract = all.filter(s => s.contract && !s.name.includes("."));
    const members = all.filter(s => s.contract && s.name.includes("."));
    const border = [...contract, ...members].sort(symbolOrder);
    const touched = rel ? all.filter(s => !s.contract && rel.has(s.id)) : [];
    const internals = (state.internals ? all.filter(s => !s.contract) : touched).sort(symbolOrder);
    const cells = cellsOf[m.id] || [];
    if (cells.length) {
      // The border is the module's contract surface at every zoom. Each
      // file's contracts sit on the stretch of border facing its cell, marked
      // by a bracket; inside, each cell holds the file's internals, drawn as
      // their kind once the cell is large enough, or earlier when shown or
      // touched by the selection.
      const C = D.layout.cell_size, detail = cellDetail();
      const r = Math.max(2.4, Math.min(5, 90 / Math.max(border.length, 1)));
      const span = cells.length > 1 ? 2 * Math.PI / cells.length * 0.84 : 2 * Math.PI;
      const byFile = new Map(cells.map(c => [c.file, []])), loose = [];
      for (const s of border) (byFile.get(s.file) || loose).push(s);
      const shownInternal = new Set(internals.map(s => s.id));
      for (const cell of cells) {
        const cx = p.x + cell.x, cy = p.y + cell.y, key = fileKey(m.id, cell.file), mine = byFile.get(cell.file);
        // A package holding its language's module file, such as go.mod, is
        // where a module begins: the file takes the first cell, at 12:00,
        // and names itself, since it declares nothing to shade it.
        if (cell.manifest) {
          const name = cell.file.split("/").pop();
          el("polygon", { class: "file-cell manifest", "data-file": key, "data-label": `${cell.file} · the Go module begins here`, tabindex: 0, role: "button", "aria-label": `Manifest ${cell.file}`, points: hexPoints(cx, cy, C).map(q => q.join(",")).join(" "), fill: color("--panel"), stroke: color("--module-stroke"), "stroke-width": 1.2, "vector-effect": "non-scaling-stroke" }, g);
          el("text", { x: cx, y: cy + C * 0.16, "text-anchor": "middle", "font-size": (C * 1.5 / Math.max(name.length, 4)).toFixed(2), "font-family": "ui-monospace, Menlo, monospace", fill: color("--ink"), "pointer-events": "none" }, g).textContent = name;
          continue;
        }
        const label = `${cell.file} · ${cell.symbols} symbol${cell.symbols === 1 ? "" : "s"}, ${mine.length} contract${mine.length === 1 ? "" : "s"}${cell.removed ? " · removed" : ""}`;
        const fileShape = el("polygon", { class: "file-cell", "data-file": key, "data-label": label, tabindex: 0, role: "button", "aria-label": `File ${cell.file}${cell.removed ? ", removed" : ""}`, points: hexPoints(cx, cy, C).map(q => q.join(",")).join(" "), fill: color(cell.removed ? "--removed" : "--accent"), "fill-opacity": cell.removed ? "0.08" : (0.04 + 0.3 * cell.symbols / crowded).toFixed(3), stroke: color(cell.removed ? "--removed" : "--hex-stroke"), "stroke-width": 0.8, "vector-effect": "non-scaling-stroke" }, g);
        if (cell.removed) fileShape.setAttribute("stroke-dasharray", "3 2");
        // Contracts run clockwise from the left end of the file's bracket,
        // and internals left to right, top to bottom, both in symbolOrder.
        const inner = all.filter(s => !s.contract && s.file === cell.file && (detail || shownInternal.has(s.id))).sort(symbolOrder);
        const cols = Math.ceil(Math.sqrt(inner.length || 1)), step = (C * 1.15) / cols;
        inner.forEach((s, i) => {
          const x = cx - C * 0.575 + step * (i % cols + 0.5), y = cy - C * 0.575 + step * (Math.floor(i / cols) + 0.5);
          placeSymbol(g, s, x, y, Math.max(1.6, Math.min(4, step / 2.6)), rel, key);
        });
        mine.forEach((s, i) => placeSymbol(g, s, ...onBorder(p.x, p.y, S, cell.angle - span / 2 + span * (i + 0.5) / mine.length), r, rel, key));
        if (cells.length > 1 && mine.length) {
          const points = [];
          for (let k = 0; k <= 16; k++) points.push(onBorder(p.x, p.y, S, cell.angle - span / 2 + span * k / 16, 0.88).join(","));
          el("polyline", { class: "file-arc", "data-file": key, "data-label": label, points: points.join(" "), fill: "none", stroke: color("--accent"), "stroke-width": 2, "stroke-linecap": "round", "vector-effect": "non-scaling-stroke" }, g);
        }
      }
      // Planned contracts have no file yet; they wait in the empty middle.
      const cols = Math.ceil(Math.sqrt(loose.length || 1)), step = (C * 1.2) / cols;
      loose.forEach((s, i) => placeSymbol(g, s, p.x - C * 0.6 + step * (i % cols + 0.5), p.y - C * 0.6 + step * (Math.floor(i / cols) + 0.5), 3, rel));
    } else {
      const r = Math.max(2.4, Math.min(5, 90 / Math.max(border.length, 1)));
      border.forEach((s, i) => placeSymbol(g, s, ...onPerimeter(p.x, p.y, S, (i + 0.5) / border.length), r, rel));
      const cols = Math.ceil(Math.sqrt(internals.length || 1)), step = (S * 1.1) / cols;
      internals.forEach((s, i) => placeSymbol(g, s, p.x - S * 0.55 + step * (i % cols + 0.5), p.y - S * 0.55 + step * (Math.floor(i / cols) + 0.5), Math.min(r, step / 3), rel));
    }
  }
}
// glowNode finds what to light up for a symbol, module or group id: the
// symbol's icon, the module's hexagon, or the collapsed group hiding it.
function glowNode(id) {
  const svg = document.getElementById("map");
  const symbol = svg.querySelector(`[data-symbol="${CSS.escape(id)}"]`);
  if (symbol) return symbol;
  const cell = svg.querySelector(`.file-cell[data-file="${CSS.escape(id)}"]`);
  if (cell) return cell;
  if (id.includes("|")) id = fileOf(id).module;
  if (groups.has(id)) { const g = svg.querySelector(`[data-group="${CSS.escape(id)}"] polygon`); if (g) return g; }
  const s = symbols.get(id), moduleId = s ? s.module : id;
  const module = svg.querySelector(`[data-module="${CSS.escape(moduleId)}"] polygon`);
  if (module) return module;
  const a = anchorOf(moduleId);
  return a && a.group ? svg.querySelector(`[data-group="${CSS.escape(a.group.id)}"] polygon`) : null;
}
function toggleGroup(id) { const g = groups.get(id); state.groupOpen[id] = isCollapsed(g); render(); inspect(); }
function ev_is(ev) { return ev.key === "Enter" || ev.key === " "; }
function placeSymbol(g, s, x, y, r, rel, file) {
  state.symbolPos.set(s.id, [x, y]);
  const cov = symbolCoverage(s);
  const label = `${s.name} · ${s.kind}${s.contract ? "" : " · internal"}${s.change ? " · " + s.change : ""}${s.design ? " · planned, not built" : ""}${cov ? " · " + coverageWords(cov) : ""}`;
  const sg = anchored(g, x, y, { "data-symbol": s.id, tabindex: 0, role: "button", "aria-label": `${s.kind} ${s.id}${s.change ? ", " + s.change : ""}`, "data-label": label, class: rel && !rel.has(s.id) ? "dim" : "" });
  const size = (state.selected && state.selected.id === s.id) ? r * 1.6 : r;
  const shape = drawShape(sg, s.kind, 0, 0, size, s.change, s.design);
  coverageRing(sg, 0, 0, size * 1.6, cov, size * 0.32);
  sg.style.cursor = "pointer";
  if (file) sg.setAttribute("data-file", file);
  const pick = ev => { ev.stopPropagation(); select({ type: "symbol", id: s.id }); };
  sg.addEventListener("click", pick);
  sg.addEventListener("keydown", ev => { if (ev_is(ev)) { ev.preventDefault(); pick(ev); } });
  return shape;
}
