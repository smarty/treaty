// The symbols view: modules and their symbols as a tree.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// ---- Symbols view ----
// The symbols view starts with every namespace. Choosing one hides its
// siblings and opens what it holds in the next column; choosing it again
// brings the siblings back and closes everything to its right. Types open
// to their members. Internals show with "Show internals".
function treeVisible(s) { return s.contract || state.internals || (state.selected && state.selected.id === s.id); }
// treeChildren lists what an item opens to: the namespaces for the root, a
// namespace's top-level symbols, or a type's members.
function treeChildren(id) {
  if (id === undefined) return [...D.modules].sort((a, b) => a.path < b.path ? -1 : a.path > b.path ? 1 : 0).map(m => ({ id: m.id, module: m }));
  if (modules.has(id)) return (byModule.get(id) || []).filter(s => treeVisible(s) && !(parentOf.has(s.id) && treeVisible(parentOf.get(s.id)))).sort(symbolOrder).map(s => ({ id: s.id, symbol: s }));
  return (members.get(id) || []).filter(treeVisible).sort(symbolOrder).map(s => ({ id: s.id, symbol: s }));
}
function treeItem(id) { return modules.has(id) ? { id, module: modules.get(id) } : { id, symbol: symbols.get(id) }; }
function treeSelection(item) { return item.module ? { type: "module", id: item.id } : { type: "symbol", id: item.id }; }
// treeShows reports whether a selection is already on screen in the tree.
function treeShows(sel) { return state.trail.includes(sel.id) || treeChildren(state.trail[state.trail.length - 1]).some(item => item.id === sel.id); }
// trailTo is the path that shows a selection among its siblings: its
// namespace, and its type for a member.
function trailTo(sel) {
  if (sel.type === "symbol") {
    const s = symbols.get(sel.id); if (!s) return [];
    const owner = parentOf.get(s.id);
    return owner && treeVisible(owner) ? [s.module, owner.id] : [s.module];
  }
  if (sel.type === "file") return [fileOf(sel.id).module];
  return [];
}
// treeIcon draws an item's icon in the map's language: a hexagon for a
// namespace, and the symbol shapes for the rest.
function treeIcon(item) {
  const svg = el("svg", { width: 16, height: 14, viewBox: "-7 -7 16 14", "aria-hidden": "true" });
  if (item.module) {
    const m = item.module;
    const hex = el("polygon", { points: hexPoints(0, 0, 6).map(q => q.join(",")).join(" "), fill: color("--module"), stroke: m.design ? color("--planned") : m.new ? color("--added") : color("--module-stroke"), "stroke-width": 1.4 }, svg);
    if (m.design || m.new) hex.setAttribute("stroke-dasharray", "2 1.5");
  } else drawShape(svg, item.symbol.kind, 0, 0, 4.6, item.symbol.change, item.symbol.design);
  return svg;
}
function treeName(item) {
  if (item.module) return item.module.path === "." ? item.module.label || item.module.path : item.module.path;
  const s = item.symbol;
  return parentOf.has(s.id) ? s.name.slice(s.name.lastIndexOf(".") + 1) : s.name;
}
function treeCaption(id) {
  if (id === undefined) return "Namespaces";
  if (modules.has(id)) return "In " + treeName(treeItem(id));
  return "Members of " + (symbols.get(id) || {}).name;
}
function buildTreeKey() {
  const key = document.getElementById("symbol-key"); key.innerHTML = "";
  const entry = (item, text) => { const span = h("span"); span.appendChild(treeIcon(item)); span.appendChild(document.createTextNode(text)); key.appendChild(span); };
  entry({ module: { path: "" } }, "namespace");
  for (const [kind, text] of [["function", "function"], ["method", "method"], ["interface", "interface"], ["type", "concrete type"], ["value", "value"]]) entry({ symbol: { kind } }, text);
  key.appendChild(h("span", {}, "› opens · choose again to close"));
}
// openTrail makes the trail end at an item's column: down to the item when
// open is true, or just above it, showing its siblings again.
function openTrail(depth, id, open) {
  state.trail = open ? [...state.trail.slice(0, depth), id] : state.trail.slice(0, depth);
  state.treeFocus = id;
  select(treeSelection(treeItem(id)));
}
function renderSymbolTree() {
  const box = document.getElementById("symbol-columns"), hadFocus = box.contains(document.activeElement);
  const cut = state.trail.findIndex(id => !modules.has(id) && !symbols.has(id));
  if (cut >= 0) state.trail = state.trail.slice(0, cut);
  buildTreeKey();
  box.innerHTML = "";
  const pointed = state.pointer && Date.now() < state.pointer.until ? state.pointer.target : null;
  const column = (caption, items, depth, open) => {
    const col = h("div", { class: "tree-col", role: "group", "aria-label": caption });
    col.appendChild(h("h2", {}, caption));
    if (!items.length) col.appendChild(h("p", { class: "empty" }, state.internals ? "Nothing here." : "No contracts here. Turn on Show internals to see the rest."));
    for (const item of items) {
      const kids = open ? 0 : treeChildren(item.id).length;
      const s = item.symbol, selected = state.selected && state.selected.id === item.id;
      const row = h("button", { type: "button", class: "tree-row" + (open ? " expanded" : "") + (selected ? " selected" : "") + (pointed === item.id ? " pointed" : "") + (s && !s.contract ? " internal" : ""), role: "treeitem", "data-tree": item.id, "aria-selected": String(!!selected), title: s ? `${s.signature || s.name}${s.change ? " · " + s.change : ""}${s.design ? " · planned, not built" : ""}` : `${item.module.path} · ${placeName(item.module)}` });
      if (open || kids) row.setAttribute("aria-expanded", String(open));
      row.appendChild(treeIcon(item));
      row.appendChild(h("span", { class: "tree-name" }, treeName(item)));
      if (kids) row.appendChild(h("span", { class: "tree-count" }, String(kids)));
      row.appendChild(h("span", { class: "tree-arrow", "aria-hidden": "true" }, open || kids ? "›" : ""));
      row.addEventListener("click", () => { if (open) openTrail(depth, item.id, false); else if (kids) openTrail(depth, item.id, true); else { state.treeFocus = item.id; select(treeSelection(item)); } });
      row.addEventListener("focus", () => { state.treeFocus = item.id; });
      row.addEventListener("keydown", ev => {
        const rows = [...col.querySelectorAll(".tree-row")], at = rows.indexOf(row);
        if (ev.key === "ArrowDown" || ev.key === "ArrowUp") { ev.preventDefault(); const next = rows[at + (ev.key === "ArrowDown" ? 1 : -1)]; if (next) next.focus(); }
        else if (ev.key === "ArrowRight") { ev.preventDefault(); if (kids) openTrail(depth, item.id, true); else if (open) { const first = col.nextElementSibling && col.nextElementSibling.querySelector(".tree-row"); if (first) first.focus(); } }
        else if (ev.key === "ArrowLeft") { ev.preventDefault(); if (open) openTrail(depth, item.id, false); else if (depth > 0) openTrail(depth - 1, state.trail[depth - 1], false); }
      });
      col.appendChild(row);
    }
    box.appendChild(col);
    return col;
  };
  state.trail.forEach((id, depth) => column(treeCaption(state.trail[depth - 1]), [treeItem(id)], depth, true));
  const last = column(treeCaption(state.trail[state.trail.length - 1]), treeChildren(state.trail[state.trail.length - 1]), state.trail.length, false);
  box.scrollLeft = Math.max(0, last.offsetLeft + last.offsetWidth - box.clientWidth);
  if (hadFocus && state.treeFocus) { const row = box.querySelector(`[data-tree="${CSS.escape(state.treeFocus)}"]`); if (row) row.focus(); }
}
