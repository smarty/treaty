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
  // Rings for hexagonal and clean; bands, boundaries, cells and islands as
  // regions for the other architectures.
  const tones = ["--ring-domain", "--ring-app", "--ring-adapter", "--ring-outer"];
  // A half ring, such as hexagonal composition, covers only the left, and
  // its label centers on the left half of its top edge. A snowflake's rings
  // are its stars, their core regions and contracts, and their leaves, each
  // at its own center, and are told apart by their place in the list, since
  // many share a layer.
  const starry = STAR_STYLES.includes(D.architecture), rings = D.layout.rings || [];
  for (let index = rings.length - 1; index >= 0; index--) {
    const ring = rings[index], x = ring.x || 0, y = ring.y || 0;
    const top = ring.radius * Math.sin(Math.PI / 3), points = ring.left ? [[x, y + top], [x - ring.radius / 2, y + top], [x - ring.radius, y], [x - ring.radius / 2, y - top], [x, y - top]] : hexPoints(x, y, ring.radius);
    const backdrop = el("polygon", { class: "backdrop", "data-band": "ring:" + (starry ? index : ring.layer), points: points.map(p => p.join(",")).join(" "), fill: color(tones[ring.tone] || tones[3]), stroke: color("--ring-stroke") }, svg);
    if (starry) backdrop.setAttribute("data-label", ringWords(ring));
    el("text", { x: 0, y: 0, "text-anchor": "middle", class: "ring-label" }, anchored(svg, ring.left ? x - ring.radius / 4 : x, y - top + 16, {}, true, true)).textContent = ring.label || ring.layer.replace(/_/g, " ");
  }
  for (const [index, r] of (D.layout.regions || []).entries()) {
    el("rect", { class: "backdrop", "data-band": "region:" + index, x: r.x, y: r.y, width: r.w, height: r.h, rx: 8, fill: color(tones[r.tone] || tones[3]), stroke: color("--ring-stroke"), "vector-effect": "non-scaling-stroke" }, svg);
    el("text", { x: 0, y: 0, class: "ring-label" }, anchored(svg, r.x + 10, r.y + 17, {}, true, true)).textContent = regionName(r);
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
    if (!pairs.has(key)) pairs.set(key, { from: a.key, to: b.key, ids: [e.from, e.to], a, b, references: [], imports: [], count: 0, violation: false, existing: true, new: false, changed: false, removed: !!e.removed });
    const pair = pairs.get(key); pair.references.push(...e.references); pair.imports.push(...(e.imports || [])); pair.count += e.count; pair.violation ||= e.violation; if (e.violation) { pair.existing &&= !!(e.existing || e.ignored); pair.ignored = e.ignored || pair.ignored; } pair.new ||= e.new; pair.changed ||= e.changed;
  }
  const edgeLayer = el("g", {}, svg);
  const maxCount = Math.max(1, ...[...pairs.values()].map(e => e.count));
  if (starry) laneEdges(pairs);
  for (const e of pairs.values()) {
    // Uses (calls, type uses) draw solid; inherits (implements, embeds) draw
    // dashed with a hollow head. A pair with both bows them to opposite sides.
    const uses = e.references.filter(r => !inherits(r.kind)), inherited = e.references.filter(r => inherits(r.kind));
    const parts = [];
    // An edge with only imports, such as a blank import, still draws as a use.
    if (uses.length || (!inherited.length && e.imports.length)) parts.push({ refs: uses, inherit: false, bend: 0.12 });
    if (inherited.length) parts.push({ refs: inherited, inherit: true, bend: uses.length ? -0.12 : 0.12 });
    parts.forEach((part, index) => {
      let from = e.a, to = e.b, bend = part.bend;
      // In a snowflake, allowed dependencies run straight, in lanes between
      // their regions, and a violation bows away from the center of the
      // star around both ends, so a sideways one is an outward chord.
      if (starry && e.violation) bend = (index ? 0.18 : 0.3) * outwardSign(e);
      else if (starry) {
        const length = Math.hypot(to.x - from.x, to.y - from.y) || 1, offset = (e.lane || 0) + index * 3;
        const px = -(to.y - from.y) / length * offset, py = (to.x - from.x) / length * offset;
        from = { ...from, x: from.x + px, y: from.y + py }; to = { ...to, x: to.x + px, y: to.y + py }; bend = 0;
      }
      const mx = (from.x + to.x) / 2, my = (from.y + to.y) / 2, dx = to.x - from.x, dy = to.y - from.y;
      const cx = mx - dy * bend, cy = my + dx * bend;
      const trim = (p, toward, by) => { const vx = toward[0] - p.x, vy = toward[1] - p.y, d = Math.hypot(vx, vy) || 1; return { x: p.x + vx / d * by, y: p.y + vy / d * by }; };
      const a = trim(from, [cx, cy], from.r + 4), b = trim(to, [cx, cy], to.r + 8);
      const stroke = e.violation ? color("--violation") : e.new ? color("--added") : e.removed ? color("--removed") : e.changed ? color("--changed") : color("--edge");
      // A violation the base already had, or one treaty.yaml ignores, stays
      // on the map, thinner and with a hollow mark, since it does not fail
      // the check.
      const old = e.violation && e.existing;
      const width = e.violation ? (old ? 1.5 : 2.5) : 1 + 3 * part.refs.length / maxCount;
      const dimmed = rel && !(relatedKey(rel, e.from) && relatedKey(rel, e.to));
      const kinds = part.inherit ? [...new Set(part.refs.map(r => r.kind))].join(", ") : "uses";
      const imported = !part.inherit && !part.refs.length ? ` · ${e.imports.length} import${e.imports.length === 1 ? "" : "s"}, no references` : "";
      const label = `${from.label} → ${to.label} · ${kinds} · ${part.refs.length} reference${part.refs.length === 1 ? "" : "s"}${imported}${e.violation ? (old ? (e.ignored ? " · ignored violation" : " · existing violation") : " · violation") + (e.rule ? ": " + e.rule : "") : ""}${e.removed ? " · removed" : e.new ? " · added" : e.changed ? " · changed" : ""}`;
      const path = el("path", { d: `M${a.x},${a.y} Q${cx},${cy} ${b.x},${b.y}`, fill: "none", stroke, "stroke-width": width, "marker-end": part.inherit ? "url(#inherit)" : "url(#arrow)", "vector-effect": "non-scaling-stroke", "data-label": label, class: dimmed && !e.violation ? "dim" : "", tabindex: 0, role: "button", "aria-label": `${part.inherit ? "Inherits" : "Dependency"} ${from.label} to ${to.label}${e.violation ? (old ? ", existing layer violation" : ", layer violation") : ""}` }, edgeLayer);
      if (part.inherit || e.removed) path.setAttribute("stroke-dasharray", e.removed ? "3 3" : "6 4");
      path.dataset.edge = e.from + "|" + e.to + (e.removed ? "|removed" : "");
      const pick = () => select({ type: "edge", from: e.from, to: e.to, removed: e.removed, edge: e });
      const hit = el("path", { d: path.getAttribute("d"), fill: "none", stroke: "transparent", "stroke-width": 14, "vector-effect": "non-scaling-stroke", "pointer-events": "stroke", "data-label": label }, edgeLayer);
      hit.glowTarget = path;
      for (const target of [path, hit]) { target.style.cursor = "pointer"; target.addEventListener("click", pick); }
      path.addEventListener("keydown", ev => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); pick(); } });
      if (e.violation && index === 0) {
        const qx = 0.25 * a.x + 0.5 * cx + 0.25 * b.x, qy = 0.25 * a.y + 0.5 * cy + 0.25 * b.y;
        const mark = anchored(edgeLayer, qx, qy, { "data-label": `${old ? "Existing layer violation, in the base too" : "Layer violation"}: ${from.label} → ${to.label}` });
        el("circle", { cx: 0, cy: 0, r: 8, fill: old ? color("--bg") : color("--violation"), stroke: color("--violation"), "stroke-width": old ? 2 : 0 }, mark);
        el("text", { x: 0, y: 4, "text-anchor": "middle", "font-size": 11, "font-weight": 700, fill: old ? color("--violation") : "#fff", "pointer-events": "none" }, mark).textContent = "!";
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
    // A snowflake's contracts carry a dot, in the middle, or below a lone
    // file's cell.
    if (starry && m.layer === "contracts") {
      const lone = (cellsOf[m.id] || []).length === 1, at = lone ? (S * Math.cos(Math.PI / 6) + (D.layout.cell_size || 0) * Math.cos(Math.PI / 6)) / 2 : 0;
      el("circle", { cx: p.x, cy: p.y + at, r: Math.max(2, S * 0.07), fill: color("--module-stroke"), "pointer-events": "none" }, g);
    }
    // Composition wires adapters into the core: a double outline marks the
    // only modules allowed to depend on adapters. In a snowflake composition
    // is orchestration, so the outline marks the orchestrators.
    if (m.layer === "composition" || starry && m.layer === "orchestrator") el("polygon", { points: hexPoints(p.x, p.y, S * 0.84).map(q => q.join(",")).join(" "), fill: "none", stroke: color("--hex-stroke"), "stroke-width": 1, "vector-effect": "non-scaling-stroke", "pointer-events": "none" }, g);
    // A boundary's public API is the only part other boundaries may use: a
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
// ringWords names a snowflake ring for people: a star, a star's core
// region, its contracts or a leaf.
function ringWords(ring) {
  if (ring.layer === "star") return `star ${ring.star}${starParent(ring.star) ? ` · branch of ${starParent(ring.star)}` : ""} · right-click to zoom to it`;
  if (ring.layer === "core") return `core of star ${ring.star}: its orchestrators, around its contracts`;
  if (ring.layer === "contracts") return `contracts of star ${ring.star}`;
  if (ring.layer === "leaf") return `leaf ${ring.spoke} of star ${ring.star}`;
  return ring.label || ring.layer;
}
// regionKey names the snowflake region a module is in: a star's contracts,
// its orchestrators, a leaf, or unclassified.
function regionKey(id) { const m = modules.get(id) || {}; return m.layer === "contracts" || m.layer === "orchestrator" ? m.layer + ":" + m.star : m.layer === "leaf" ? "leaf:" + m.spoke : m.layer || ""; }
// laneEdges gives each allowed dependency in a snowflake its lane: the
// dependencies between the same two regions run side by side, 5 units
// apart, so they read as one bundle down an arm. Lanes are measured the
// same way round whichever way a dependency runs, so none share a lane.
function laneEdges(pairs) {
  const bundles = new Map();
  for (const e of pairs.values()) {
    if (e.violation) continue;
    const a = regionKey(e.ids[0]), b = regionKey(e.ids[1]), key = a < b ? a + "\u0000" + b : b + "\u0000" + a;
    if (!bundles.has(key)) bundles.set(key, []);
    bundles.get(key).push(e);
    e.flip = a > b;
  }
  for (const bundle of bundles.values()) bundle.forEach((e, i) => { e.lane = (i - (bundle.length - 1) / 2) * 5 * (e.flip ? -1 : 1); });
}
// outwardSign picks the side a violation bows to: away from the center of
// the deepest star around both its ends.
function outwardSign(e) {
  const a = modules.get(e.ids[0]) || {}, b = modules.get(e.ids[1]) || {};
  const ring = starRing(commonStar(a.star || "", b.star || "")) || {}, x = ring.x || 0, y = ring.y || 0;
  const mx = (e.a.x + e.b.x) / 2, my = (e.a.y + e.b.y) / 2, dx = e.b.x - e.a.x, dy = e.b.y - e.a.y;
  return -dy * (mx - x) + dx * (my - y) >= 0 ? 1 : -1;
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

// ---- Regions ----
// A region's name is the label the layout gives it, a layer or a
// boundary, unless the person gave it another. Names given are kept per
// architecture and are only for the map: treaty.yaml, the rules and every
// check keep the region's own label. A boundary has no name of the map's
// own: renaming it renames it in treaty.yaml.
function regionName(r) { return isBoundaryRegion(r) ? r.label : ((state.regionNames || {})[D.architecture] || {})[r.label] || r.label; }
// isBoundaryRegion reports whether a region draws a whole boundary, a
// column of vertical or an island of modular, rather than one of its cells.
function isBoundaryRegion(r) { return !!r.boundary && (!r.layer || r.layer === "boundary"); }
// regionAt finds the region under a map point, and its index.
function regionAt(x, y) {
  let hit = -1;
  (D.layout.regions || []).forEach((r, i) => { if (x >= r.x && x <= r.x + r.w && y >= r.y && y <= r.y + r.h) hit = i; });
  return hit < 0 ? null : { region: D.layout.regions[hit], index: hit };
}
// regionContextMenu opens a region's menu on a right click inside it, away
// from its modules.
function regionContextMenu(ev) {
  if (ev.defaultPrevented || carry || state.mapTab !== "references" || !D) return;
  if (ev.target.closest && ev.target.closest("[data-module], [data-group], [data-symbol]")) return;
  if (STAR_STYLES.includes(D.architecture)) {
    const p = toMap(ev.clientX, ev.clientY), band = bandAt(p.x, p.y);
    if (!band || !band.star) return;
    ev.preventDefault();
    starMenu(band.star, ev.clientX, ev.clientY);
    return;
  }
  if (!(D.layout.regions || []).length) return;
  const p = toMap(ev.clientX, ev.clientY), hit = regionAt(p.x, p.y);
  if (!hit) return;
  ev.preventDefault();
  regionMenu(hit, ev.clientX, ev.clientY);
}
function regionMenu({ region, index }, x, y) {
  const name = regionName(region), renamed = name !== region.label;
  const moved = [...state.moved].filter(id => { const p = D.layout.modules[id], at = p && regionAt(p.x, p.y); return at && at.index === index; });
  const items = [];
  const previewing = live.state && live.state.view && live.state.view.architecture !== live.state.view.configured;
  if (LIVE && isBoundaryRegion(region)) {
    items.push({ label: "Rename boundary…", disabled: previewing, title: previewing ? "This is a preview; keep or adopt it before renaming boundaries" : "Rename this boundary in treaty.yaml", action: () => openPrompt(x, y, `Rename boundary “${region.boundary}”`, region.boundary, value => renameBoundary(region.boundary, value.trim())) });
    items.push("-");
  } else if (LIVE) {
    items.push({ label: "Rename…", title: "Give this region another name on the map; treaty.yaml keeps its own", action: () => renameRegion(region, x, y) });
    items.push({ label: `Restore the name “${region.label}”`, disabled: !renamed, title: renamed ? "" : "This region has its own name", action: () => nameRegion(region, "") });
    items.push("-");
  }
  items.push({ label: "Zoom to this region", action: () => zoomToRect(region) });
  if (LIVE) items.push({ label: moved.length ? `Put its ${moved.length} moved module${moved.length === 1 ? "" : "s"} back` : "Put its moved modules back", disabled: !moved.length, title: moved.length ? "Return them to where the layout places them" : "No module here has been moved", action: () => { for (const id of moved) savePosition(id, null); } });
  openMenu(x, y, "Region " + name, items);
}
// starMenu opens a snowflake star's menu. A star is named by its
// directory, so it has no Rename.
function starMenu(dir, x, y) {
  const ring = starRing(dir); if (!ring) return;
  const at = { x: (ring.x || 0) - ring.radius, y: (ring.y || 0) - ring.radius, w: 2 * ring.radius, h: 2 * ring.radius };
  const moved = [...state.moved].filter(id => { const m = modules.get(id); return m && m.star && starsAround(m.star).includes(dir); });
  const items = [{ label: "Zoom to this star", action: () => zoomToRect(at) }];
  if (LIVE) items.push({ label: moved.length ? `Put its ${moved.length} moved module${moved.length === 1 ? "" : "s"} back` : "Put its moved modules back", disabled: !moved.length, title: moved.length ? "Return them to where the layout places them" : "No module here has been moved", action: () => { for (const id of moved) savePosition(id, null); } });
  openMenu(x, y, "Star " + dir, items);
}
function renameRegion(region, x, y) {
  openPrompt(x, y, `Rename “${regionName(region)}”`, regionName(region), value => nameRegion(region, value.trim()));
}
// nameRegion gives a region a name on this map, or with "" its own label
// back.
async function nameRegion(region, name) {
  const style = D.architecture, before = state.regionNames;
  const names = { ...(state.regionNames[style] || {}) };
  if (!name || name === region.label) delete names[region.label]; else names[region.label] = name;
  state.regionNames = { ...state.regionNames, [style]: names };
  render();
  const saved = await saveMapSettings({ names: { [style]: { [region.label]: name === region.label ? "" : name } } });
  if (saved) { state.regionNames = saved.names || {}; render(); return; }
  state.regionNames = before; render();
  setStatus("could not save the region's name", true);
}
// renameBoundary renames a boundary in treaty.yaml; the map, the checks and
// agents follow once the server has rebuilt.
async function renameBoundary(from, to) {
  if (!to || to === from) return;
  setStatus(`renaming boundary ${from} to ${to}…`);
  try {
    const response = await fetch("api/boundary", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ from, to }) });
    if (!response.ok) throw new Error((await response.text()).trim());
    setStatus("");
    showState(await response.json());
  } catch (err) {
    setStatus(`not renamed: ${err.message}`, true);
  }
}
// zoomToRect fits the view around a rectangle of the map, keeping the
// view's shape.
function zoomToRect(r) {
  const v = state.view || home(), ratio = v.h / v.w, pad = 1.08;
  const w = Math.max(r.w, r.h / ratio) * pad, h = w * ratio;
  state.view = { x: r.x + r.w / 2 - w / 2, y: r.y + r.h / 2 - h / 2, w, h };
  applyView();
}
