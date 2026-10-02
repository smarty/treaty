// The symbols view: modules and their symbols as a tree.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// ---- Symbols view ----
// The symbols view reveals what a caller can reach, the way code completion
// does after a dot: it starts at the repository's root, and each package
// opens to its subpackages and its symbols, each type to its members.
// Packages no caller outside the repository can import, such as Go internal
// and main packages, are left out, along with internals, until "Show
// internals" is on. Choosing an item hides its siblings and opens it in the
// next column; choosing it again brings them back. The tree shows one
// language at a time, since each language names and reaches its code its
// own way; JavaScript and TypeScript, which import each other, count as one.
const DIR = "dir:";
const LANGUAGE_NAMES = { go: "Go", js: "JavaScript", py: "Python", ts: "TypeScript" };
let dirs = new Map(), languages = [], familyNames = new Map(), typeOfValue = new Map(), valueOfType = new Map();
function dirId(path) { return DIR + path; }
function parentPath(path) { const at = path.lastIndexOf("/"); return at < 0 ? "." : path.slice(0, at); }
// buildDirs makes the tree's directories from the modules' paths: every
// module's directory and each directory above it, up to the root.
function buildDirs() {
  dirs = new Map();
  const ensure = path => {
    if (!dirs.has(path)) {
      dirs.set(path, { path, modules: [], children: new Set() });
      if (path !== ".") ensure(parentPath(path)).children.add(path);
    }
    return dirs.get(path);
  };
  ensure(".");
  for (const m of D.modules) ensure(m.path).modules.push(m);
  // The languages go most modules first, so the tree opens on the one most
  // of the repository is written in.
  const counts = new Map();
  for (const m of D.modules) counts.set(languageFamily(m.language), (counts.get(languageFamily(m.language)) || 0) + 1);
  languages = [...counts.keys()].sort((a, b) => counts.get(b) - counts.get(a) || a.localeCompare(b));
  familyNames = new Map();
  for (const m of D.modules) { const names = familyNames.get(languageFamily(m.language)) || new Set(); names.add(languageName(m.language)); familyNames.set(languageFamily(m.language), names); }
  buildValueTypes();
}
// buildValueTypes links each Go value to the type it is declared as, when
// that type's methods are what a caller reaches after the value's dot, as
// with an options namespace: var Compose composeOptions.
function buildValueTypes() {
  typeOfValue = new Map(); valueOfType = new Map();
  for (const s of D.symbols) {
    const m = modules.get(s.module); if (s.kind !== "value" || !m || m.language !== "go") continue;
    const found = /^var \S+ \*?([A-Za-z_]\w*)$/.exec(s.signature || ""); if (!found) continue;
    const t = (byModule.get(s.module) || []).find(p => p.name === found[1] && (p.kind === "type" || p.kind === "interface"));
    if (!t || !members.has(t.id)) continue;
    typeOfValue.set(s.id, t);
    if (s.contract && !valueOfType.has(t.id)) valueOfType.set(t.id, s);
  }
}
try { state.treeLanguage = localStorage.getItem("treaty.treeLanguage"); } catch (err) { /* storage unavailable */ }
function languageName(language) { return LANGUAGE_NAMES[language] || language.charAt(0).toUpperCase() + language.slice(1); }
function languageFamily(language) { return language === "ts" ? "js" : language; }
// familyName names a family by the languages the repository writes in it,
// such as "JavaScript / TypeScript".
function familyName(family) { return [...(familyNames.get(family) || [languageName(family)])].sort().join(" / "); }
// treeLanguage is the language the tree shows: the one chosen, else the one
// most of the repository is written in.
function treeLanguage() { return languages.includes(state.treeLanguage) ? state.treeLanguage : languages[0]; }
// importable reports whether code outside the repository can import a
// module: it is not an entry point, such as a Go main package, or private,
// such as a Go internal package.
function importable(m) { return !m.entry && !m.private; }
function moduleShown(m) { return languageFamily(m.language) === treeLanguage() && (state.internals || importable(m)); }
function treeVisible(s) { return s.contract || state.internals || (state.selected && state.selected.id === s.id); }
// dirShown reports whether a directory holds anything the tree shows: a
// shown module, or a directory beneath it that does.
function dirShown(path) { const d = dirs.get(path); return !!d && (d.modules.some(moduleShown) || [...d.children].some(dirShown)); }
function topSymbols(moduleId) { return (byModule.get(moduleId) || []).filter(s => treeVisible(s) && !(parentOf.has(s.id) && treeVisible(parentOf.get(s.id)))); }
// treeChildren lists what an item opens to: the root for the first column,
// a package's subpackages and then its top-level symbols, or a type's
// members. A value opens to the members of the type it is declared as; an
// exported member reached through an exported value is there for callers
// even when its type is not.
function treeChildren(id) {
  if (id === undefined) return [{ id: dirId("."), dir: dirs.get(".") }];
  if (id.startsWith(DIR)) {
    const d = dirs.get(id.slice(DIR.length)); if (!d) return [];
    const subs = [...d.children].filter(dirShown).sort().map(path => ({ id: dirId(path), dir: dirs.get(path) }));
    const own = d.modules.filter(moduleShown).flatMap(m => topSymbols(m.id)).sort(symbolOrder).map(s => ({ id: s.id, symbol: s }));
    return [...subs, ...own];
  }
  const own = (members.get(id) || []).filter(treeVisible).map(s => ({ id: s.id, symbol: s }));
  const value = symbols.get(id), type = typeOfValue.get(id);
  const reached = type ? (members.get(type.id) || []).filter(s => treeVisible(s) || (value.contract && exportedMember(s))).map(s => ({ id: s.id, symbol: s, reached: !s.contract && value.contract })) : [];
  return [...own, ...reached].sort((a, b) => symbolOrder(a.symbol, b.symbol));
}
function exportedMember(s) { const name = s.name.slice(s.name.lastIndexOf(".") + 1); return name.charAt(0) !== name.charAt(0).toLowerCase(); }
function treeItem(id) { return id.startsWith(DIR) ? { id, dir: dirs.get(id.slice(DIR.length)) } : { id, symbol: symbols.get(id) }; }
// dirModule is the module a directory item stands for: the first one the
// tree shows, or any in the tree's language.
function dirModule(d) { return d.modules.find(moduleShown) || d.modules.find(m => languageFamily(m.language) === treeLanguage()); }
function treeSelection(item) {
  if (item.symbol) return { type: "symbol", id: item.id };
  const m = dirModule(item.dir);
  return m ? { type: "module", id: m.id } : null;
}
// treeKey is the tree item a selection is: a module's directory, or the
// symbol itself.
function treeKey(sel) {
  if (sel.type !== "module") return sel.id;
  const m = modules.get(sel.id);
  return m ? dirId(m.path) : sel.id;
}
function itemSelected(item) { return !!state.selected && treeKey(state.selected) === item.id; }
// treeShows reports whether a selection is already on screen in the tree.
function treeShows(sel) { const key = treeKey(sel); return state.trail.includes(key) || treeChildren(state.trail[state.trail.length - 1]).some(item => item.id === key); }
// dirChain lists the directories from the root down to a path.
function dirChain(path) { const result = []; for (let at = path; ; at = parentPath(at)) { result.unshift(dirId(at)); if (at === ".") break; } return result; }
// trailTo is the path that shows a selection among its siblings: the
// directories down to its package, and its type for a member, or the value
// a caller reaches it through when its type is hidden. A selection in
// another language turns the tree to that language.
function trailTo(sel) {
  const m = sel.type === "symbol" ? symbols.has(sel.id) && modules.get(symbols.get(sel.id).module) : sel.type === "module" ? modules.get(sel.id) : sel.type === "file" ? modules.get(fileOf(sel.id).module) : null;
  if (!m) return [];
  if (languageFamily(m.language) !== treeLanguage()) state.treeLanguage = languageFamily(m.language);
  if (sel.type === "symbol") {
    const owner = parentOf.get(sel.id), via = owner && valueOfType.get(owner.id);
    if (owner && treeVisible(owner)) return [...dirChain(m.path), owner.id];
    return via && treeVisible(via) ? [...dirChain(m.path), via.id] : dirChain(m.path);
  }
  if (sel.type === "module") return m.path !== "." ? dirChain(parentPath(m.path)) : [];
  return dirChain(m.path);
}
// treeIcon draws an item's icon in the map's language: a hexagon for a
// package, a dashed one for a directory that only holds packages, and the
// symbol shapes for the rest.
function treeIcon(item) {
  const svg = el("svg", { width: 16, height: 14, viewBox: "-7 -7 16 14", "aria-hidden": "true" });
  if (item.dir) {
    const m = dirModule(item.dir);
    const hex = el("polygon", { points: hexPoints(0, 0, 6).map(q => q.join(",")).join(" "), fill: m ? color("--module") : "none", stroke: !m ? color("--hex-stroke") : m.design ? color("--planned") : m.new ? color("--added") : color("--module-stroke"), "stroke-width": 1.4 }, svg);
    if (!m || m.design || m.new) hex.setAttribute("stroke-dasharray", "2 1.5");
  } else {
    drawShape(svg, item.symbol.kind, 0, 0, 4.6, item.symbol.change, item.symbol.design);
    coverageRing(svg, 0, 0, 6.4, symbolCoverage(item.symbol), 1.1);
  }
  return svg;
}
// treeName names an item as a caller writes it: a package by its name,
// such as its Go package name, else its directory; the root by its
// package's name, else the repository's. An entry point, such as a Go main
// package, goes by its directory, since nothing refers to it by name.
function treeName(item) {
  if (item.dir) {
    const d = item.dir, m = dirModule(d);
    if (m && m.name && !m.entry) return m.name;
    return d.path === "." ? D.root || "repository" : d.path.slice(d.path.lastIndexOf("/") + 1);
  }
  const s = item.symbol;
  return parentOf.has(s.id) ? s.name.slice(s.name.lastIndexOf(".") + 1) : s.name;
}
function treeCaption(id) {
  if (id === undefined) return "Repository";
  if (id.startsWith(DIR)) return "In " + treeName(treeItem(id));
  return "Members of " + (symbols.get(id) || {}).name;
}
function buildTreeKey() {
  const key = document.getElementById("symbol-key"); key.innerHTML = "";
  if (languages.length > 1) {
    const choice = h("div", { class: "tree-languages", role: "tablist", "aria-label": "Language" });
    for (const language of languages) {
      const tab = h("button", { type: "button", class: "map-tab", role: "tab", "aria-selected": String(language === treeLanguage()), title: `Show the ${familyName(language)} code` }, familyName(language));
      tab.addEventListener("click", () => {
        state.treeLanguage = language;
        try { localStorage.setItem("treaty.treeLanguage", language); } catch (err) { /* storage unavailable */ }
        render();
      });
      choice.appendChild(tab);
    }
    key.appendChild(choice);
  }

  const entry = (item, text) => { const span = h("span"); span.appendChild(treeIcon(item)); span.appendChild(document.createTextNode(text)); key.appendChild(span); };
  entry({ dir: { path: "", modules: [{ path: "", language: languages.length ? treeLanguage() : "" }] } }, "package");
  entry({ dir: { path: "", modules: [] } }, "directory");
  for (const [kind, text] of [["function", "function"], ["method", "method"], ["interface", "interface"], ["type", "concrete type"], ["value", "value"]]) entry({ symbol: { kind } }, text);
  key.appendChild(h("span", {}, "› opens · choose again to close"));
}
// openTrail makes the trail end at an item's column: down to the item when
// open is true, or just above it, showing its siblings again.
function openTrail(depth, id, open) {
  state.trail = open ? [...state.trail.slice(0, depth), id] : state.trail.slice(0, depth);
  state.treeFocus = id;
  const sel = treeSelection(treeItem(id));
  if (sel) select(sel); else render();
}
function renderSymbolTree() {
  const box = document.getElementById("symbol-columns"), hadFocus = box.contains(document.activeElement);
  // A trail must start at the root and pass only through what is shown.
  const cut = state.trail.findIndex((id, depth) => depth === 0 ? id !== dirId(".") : id.startsWith(DIR) ? !dirShown(id.slice(DIR.length)) : !symbols.has(id));
  if (cut >= 0) state.trail = state.trail.slice(0, cut);
  buildTreeKey();
  box.innerHTML = "";
  const pointed = state.pointer && Date.now() < state.pointer.until ? state.pointer.target : null;
  const column = (caption, items, depth, open) => {
    const col = h("div", { class: "tree-col", role: "group", "aria-label": caption });
    col.appendChild(h("h2", {}, caption));
    if (!items.length) col.appendChild(h("p", { class: "empty" }, state.internals ? "Nothing here." : "Nothing here can be used from outside this repository. Turn on Show internals to see the rest."));
    for (const item of items) {
      const kids = open ? 0 : treeChildren(item.id).length;
      const s = item.symbol, m = item.dir && dirModule(item.dir), selected = itemSelected(item);
      const hiddenFromCallers = s ? !s.contract && !item.reached : !item.dir.modules.some(importable);
      const where = item.dir && (item.dir.path === "." ? "repository root" : item.dir.path);
      const title = s ? `${s.signature || s.name}${s.change ? " · " + s.change : ""}${s.design ? " · planned, not built" : ""}` : `${where}${m ? " · " + placeName(m) : " · directory"}${m && !importable(m) ? (m.entry ? " · an entry point, not importable" : " · private to this repository") : ""}`;
      const row = h("button", { type: "button", class: "tree-row" + (open ? " expanded" : "") + (selected ? " selected" : "") + (pointed && treeKey(selectionOf(pointed) || { id: pointed }) === item.id ? " pointed" : "") + (hiddenFromCallers ? " internal" : ""), role: "treeitem", "data-tree": item.id, "aria-selected": String(selected), title });
      if (open || kids) row.setAttribute("aria-expanded", String(open));
      row.appendChild(treeIcon(item));
      row.appendChild(h("span", { class: "tree-name" }, treeName(item)));
      if (kids) row.appendChild(h("span", { class: "tree-count" }, String(kids)));
      row.appendChild(h("span", { class: "tree-arrow", "aria-hidden": "true" }, open || kids ? "›" : ""));
      row.addEventListener("click", () => { if (open) openTrail(depth, item.id, false); else if (kids) openTrail(depth, item.id, true); else { state.treeFocus = item.id; const sel = treeSelection(item); if (sel) select(sel); } });
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
