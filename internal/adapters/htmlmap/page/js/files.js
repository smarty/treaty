// Switching between map views, and the files view: the tree as nested
// directory hexagons.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// ---- Map views ----
// setMapTab shows one of the map's views. Each view keeps its own camera,
// and the choice is kept with the layout.
function setMapTab(tab, save = true) {
  if (!MAP_TABS[tab]) tab = "references";
  if (carry) putBack("");
  if (tab !== state.mapTab) { state.views[state.mapTab] = state.view; state.view = state.views[tab] || null; state.mapTab = tab; }
  for (const button of document.querySelectorAll(".map-tab")) { const on = button.dataset.mapTab === tab; button.setAttribute("aria-selected", String(on)); button.tabIndex = on ? 0 : -1; }
  document.querySelector(".map-wrap").hidden = tab === "symbols";
  document.getElementById("symbol-tree").hidden = tab !== "symbols";
  tip.style.display = "none"; setGlow(null);
  // The data is indexed by ingest; before that there is nothing to draw.
  if (modules && tab === "symbols" && state.selected && !treeShows(state.selected)) state.trail = trailTo(state.selected);
  dock.mapTab = tab;
  if (save) saveLayout();
  if (modules) { render(); inspect(); }
}

// ---- Files view ----
// The files view draws the repository as it sits on disk: each directory a
// hexagon holding its subdirectories and files, packed tight, and each file
// a small hexagon shaded by how many symbols it declares. Names show once
// they are large enough to read, and a file's symbols once there is room.
const FILE_R = 10, FILE_GAP = 2, DIR_PAD = 5, DIR_LABEL_ROOM = 10, PACK_EXACT_MAX = 60;
const FILE_NAME_PX = 22, FILE_SYMBOLS_PX = 70, DIR_LABEL_PX = 45;
let filesLayout = null;
// layoutFiles builds the directory tree from the modules' files and places
// it, innermost first, around the origin.
function layoutFiles() {
  const root = { name: "", path: "", dirs: new Map(), files: [] };
  const dirAt = path => {
    let node = root, at = "";
    if (!path) return node;
    for (const part of path.split("/")) {
      at = at ? at + "/" + part : part;
      if (!node.dirs.has(part)) node.dirs.set(part, { name: part, path: at, dirs: new Map(), files: [] });
      node = node.dirs.get(part);
    }

    return node;
  };
  const bySource = new Map();
  for (const s of D.symbols) if (s.file) { if (!bySource.has(s.file)) bySource.set(s.file, []); bySource.get(s.file).push(s); }
  for (const m of D.modules) {
    const path = m.path === "." ? "" : m.path, gone = new Set(m.removed_files || []);
    const own = [...(m.manifest ? [m.manifest] : []), ...(m.files || []), ...gone];
    const dir = dirAt(path);
    dir.module = m.id;
    for (const file of own) {
      const at = file.lastIndexOf("/");
      dirAt(at < 0 ? "" : file.slice(0, at)).files.push({ path: file, name: file.slice(at + 1), module: m.id, manifest: file === m.manifest, removed: !!m.removed || gone.has(file), symbols: (bySource.get(file) || []).filter(s => s.module === m.id), r: FILE_R });
    }
  }
  const groupByPath = new Map((D.layout.groups || []).map(g => [g.path, g.id]));
  const dirs = [], files = [], byKey = new Map(), moduleDirs = new Map(), byGroup = new Map();
  const measure = node => {
    const children = [...[...node.dirs.values()].map(d => measure(d)), ...node.files];
    children.sort((a, b) => b.r - a.r || (!!b.dirs - !!a.dirs) || (a.name < b.name ? -1 : 1));
    const R = children.length ? packCircles(children) : FILE_R;
    for (const c of children) c.y += DIR_LABEL_ROOM / 2;
    node.children = children;
    node.inner = R + DIR_PAD + DIR_LABEL_ROOM / 2;
    node.r = node.inner / Math.cos(Math.PI / 6);
    node.group = groupByPath.get(node.path);
    return node;
  };
  const place = (node, x, y) => {
    node.x = x; node.y = y; dirs.push(node);
    if (node.module) moduleDirs.set(node.module, node);
    if (node.group) byGroup.set(node.group, node);
    for (const c of node.children) {
      if (c.dirs) { place(c, x + c.x, y + c.y); continue; }
      c.x += x; c.y += y; files.push(c); byKey.set(fileKey(c.module, c.path), c);
    }
  };
  measure(root);
  place(root, 0, 0);
  filesLayout = { root, dirs, files, byKey, byModule: moduleDirs, byGroup, extent: root.r };
}
// packCircles places circles of radius r, largest first, each where it keeps
// the whole closest to the middle without touching another, then centers
// them. Past PACK_EXACT_MAX circles it lays them on a honeycomb instead.
//
// Parameters:
//   - items: circles with r; their x and y are set.
//
// Returns:
//   - result: the radius of the circle holding them all.
function packCircles(items) {
  const placed = [];
  if (items.length > PACK_EXACT_MAX) {
    const step = 2 * items[0].r + FILE_GAP;
    let ring = 0, index = 0;
    for (const item of items) {
      if (index >= Math.max(1, ring * 6)) { ring++; index = 0; }
      const side = ring ? Math.floor(index / ring) : 0, along = ring ? index % ring : 0;
      const a = hexPoints(0, 0, ring * step)[side], b = hexPoints(0, 0, ring * step)[(side + 1) % 6];
      item.x = a[0] + (b[0] - a[0]) * along / (ring || 1); item.y = a[1] + (b[1] - a[1]) * along / (ring || 1);
      index++;
    }
  } else {
    const clear = (x, y, r) => placed.every(p => Math.hypot(p.x - x, p.y - y) >= p.r + r + FILE_GAP - 1e-6);
    for (const item of items) {
      let best = null, score = Infinity;
      const consider = (x, y) => { const d = Math.hypot(x, y) + item.r; if (d < score && clear(x, y, item.r)) { score = d; best = [x, y]; } };
      if (!placed.length) best = [0, 0];
      for (const a of placed) {
        const d = a.r + item.r + FILE_GAP;
        for (let k = 0; k < 12; k++) consider(a.x + d * Math.cos(k * Math.PI / 6), a.y + d * Math.sin(k * Math.PI / 6));
      }
      for (let i = 0; i < placed.length; i++) for (let j = i + 1; j < placed.length; j++) {
        const a = placed[i], b = placed[j], da = a.r + item.r + FILE_GAP, db = b.r + item.r + FILE_GAP;
        const dx = b.x - a.x, dy = b.y - a.y, d = Math.hypot(dx, dy);
        if (!d || d > da + db || d < Math.abs(da - db)) continue;
        const l = (da * da - db * db + d * d) / (2 * d), t = Math.sqrt(Math.max(0, da * da - l * l));
        const mx = a.x + dx * l / d, my = a.y + dy * l / d;
        consider(mx - dy * t / d, my + dx * t / d);
        consider(mx + dy * t / d, my - dx * t / d);
      }
      item.x = best[0]; item.y = best[1];
      placed.push(item);
    }
  }
  const xs = items.map(c => [c.x - c.r, c.x + c.r]).flat(), ys = items.map(c => [c.y - c.r, c.y + c.r]).flat();
  const cx = (Math.min(...xs) + Math.max(...xs)) / 2, cy = (Math.min(...ys) + Math.max(...ys)) / 2;
  let R = 0;
  for (const c of items) { c.x -= cx; c.y -= cy; R = Math.max(R, Math.hypot(c.x, c.y) + c.r); }
  return R;
}
// fileRelated reports whether a file belongs with the selection, so the
// others dim.
function fileRelated(rel, f) {
  if (!rel) return true;
  const sel = state.selected;
  if (rel.has(fileKey(f.module, f.path)) || f.symbols.some(s => rel.has(s.id))) return true;
  return (sel.type === "module" || sel.type === "group") && rel.has(f.module);
}
function drawFiles(svg, rel) {
  const L = filesLayout, ppu = pixelsPerUnit();
  const crowded = Math.max(1, ...L.files.map(f => f.symbols.length));
  for (const d of L.dirs) {
    const m = d.module && modules.get(d.module), group = !m && d.group;
    const selected = state.selected && (state.selected.id === d.module || state.selected.id === d.group) && ["module", "group"].includes(state.selected.type);
    const name = d === L.root ? (D.title || "repository") : d.name + "/";
    const fileCount = d.children.filter(c => !c.dirs).length, dirCount = d.children.length - fileCount;
    const attrs = m ? { "data-module": m.id, tabindex: 0, role: "button", "aria-label": `Directory ${d.path || name}, module` } : group ? { "data-group": group, tabindex: 0, role: "button", "aria-label": `Directory ${d.path}` } : {};
    const node = el("g", { ...attrs, class: m || group ? "dir" : "", "data-label": `${d.path ? d.path + "/" : name} · ${fileCount} file${fileCount === 1 ? "" : "s"}, ${dirCount} director${dirCount === 1 ? "y" : "ies"}${m ? " · module " + (m.label || m.path) : ""}${m && m.design ? " · planned, not built" : ""}` }, svg);
    const hex = el("polygon", { class: m || group ? "" : "backdrop", points: hexPoints(d.x, d.y, d.r).map(q => q.join(",")).join(" "), fill: color("--group"), stroke: selected ? color("--selection") : m && m.design ? color("--planned") : m && m.new ? color("--added") : m && m.removed ? color("--removed") : color(m ? "--module-stroke" : "--hex-stroke"), "stroke-width": selected ? 2.5 : m ? 1.4 : 1, "vector-effect": "non-scaling-stroke" }, node);
    if (m && (m.design || m.new || m.removed)) hex.setAttribute("stroke-dasharray", "5 3");
    if (d === L.root || d.r * ppu >= DIR_LABEL_PX) el("text", { x: 0, y: 0, "text-anchor": "middle", class: "group-name" + (selected ? " selected-label" : "") }, anchored(node, d.x, d.y - d.inner + 12, {}, true, true)).textContent = name;
    const pick = () => { if (m) select({ type: "module", id: m.id }); else if (group) select({ type: "group", id: group }); };
    if (m || group) {
      node.style.cursor = "pointer";
      node.addEventListener("click", ev => { if (ev.target.closest("[data-symbol]")) return; ev.stopPropagation(); pick(); });
      node.addEventListener("keydown", ev => { if (ev_is(ev)) { ev.preventDefault(); pick(); } });
    }
  }
  const names = FILE_R * ppu >= FILE_NAME_PX, detail = FILE_R * ppu >= FILE_SYMBOLS_PX;
  for (const f of L.files) {
    const key = fileKey(f.module, f.path), contracts = f.symbols.filter(s => s.contract).length;
    const g = el("g", { class: fileRelated(rel, f) ? "" : "dim" }, svg);
    const label = (f.manifest ? `${f.path} · the Go module begins here` : `${f.path} · ${f.symbols.length} symbol${f.symbols.length === 1 ? "" : "s"}, ${contracts} contract${contracts === 1 ? "" : "s"}`) + (f.removed ? " · removed" : "");
    const shape = el("polygon", { class: "file-cell" + (f.manifest ? " manifest" : ""), "data-file": key, "data-label": label, tabindex: 0, role: "button", "aria-label": `File ${f.path}`, points: hexPoints(f.x, f.y, FILE_R).map(q => q.join(",")).join(" "), "vector-effect": "non-scaling-stroke" }, g);
    if (f.manifest) Object.entries({ fill: color("--panel"), stroke: color("--module-stroke"), "stroke-width": 1.2 }).forEach(([k, v]) => shape.setAttribute(k, v));
    else if (f.removed) Object.entries({ fill: color("--removed"), "fill-opacity": "0.08", stroke: color("--removed"), "stroke-width": 0.8, "stroke-dasharray": "3 2" }).forEach(([k, v]) => shape.setAttribute(k, v));
    else Object.entries({ fill: color("--accent"), "fill-opacity": (0.04 + 0.3 * f.symbols.length / crowded).toFixed(3), stroke: color("--hex-stroke"), "stroke-width": 0.8 }).forEach(([k, v]) => shape.setAttribute(k, v));
    const pick = () => select({ type: "file", id: key });
    shape.style.cursor = "pointer";
    shape.addEventListener("click", ev => { ev.stopPropagation(); pick(); });
    shape.addEventListener("keydown", ev => { if (ev_is(ev)) { ev.preventDefault(); pick(); } });
    const shown = detail && !f.manifest ? [...f.symbols].sort(symbolOrder) : [];
    if (names || f.manifest) {
      const size = Math.min(FILE_R * 0.3, FILE_R * 1.6 / Math.max(f.name.length, 4));
      el("text", { x: f.x, y: shown.length ? f.y - FILE_R * 0.42 : f.y + size * 0.35, "text-anchor": "middle", "font-size": size.toFixed(2), "font-family": "ui-monospace, Menlo, monospace", fill: color("--ink"), "pointer-events": "none" }, g).textContent = f.name;
    }
    // A file's symbols fill the lower part of its hexagon, left to right and
    // top to bottom, in symbolOrder.
    if (shown.length) {
      const cols = Math.ceil(Math.sqrt(shown.length * 1.8)), rows = Math.ceil(shown.length / cols);
      const step = Math.min(FILE_R * 1.3 / cols, FILE_R * 0.95 / rows), top = f.y - FILE_R * 0.2;
      shown.forEach((s, i) => placeSymbol(g, s, f.x - cols * step / 2 + step * (i % cols + 0.5), top + step * (Math.floor(i / cols) + 0.5), Math.min(step / 2.6, 1.6), rel, key));
    }
  }
}
// filesPositionOf is where a selection sits in the files view: a symbol's
// icon or its file, a file, or a module's or group's directory.
function filesPositionOf(sel) {
  const L = filesLayout; if (!L) return null;
  const at = node => node && { x: node.x, y: node.y };
  if (sel.type === "symbol") {
    const own = state.symbolPos.get(sel.id); if (own) return { x: own[0], y: own[1] };
    const s = symbols.get(sel.id); if (!s) return null;
    return at(s.file && L.byKey.get(fileKey(s.module, s.file))) || at(L.byModule.get(s.module));
  }
  if (sel.type === "file") return at(L.byKey.get(sel.id));
  if (sel.type === "group") return at(L.byGroup.get(sel.id));
  const moduleId = moduleOfSelection(sel);
  return moduleId ? at(L.byModule.get(moduleId)) : null;
}
