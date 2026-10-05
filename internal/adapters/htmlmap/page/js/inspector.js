// Selecting things, the inspector, the Code tab and the review queue.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// select changes what the inspector shows and what glows, and never moves
// the view or opens groups: a selection hidden in a collapsed group lights
// that group, and the inspector's Go to moves there when you ask.
function select(sel) {
  state.selected = sel;
  if (LIVE) postSelection(sel);
  // The symbols view opens the path to a selection made elsewhere, such as
  // from the inspector or the review queue.
  if (state.mapTab === "symbols" && sel && !treeShows(sel)) state.trail = trailTo(sel);
  render(); inspect(); highlightQueue();
}
// selectionOf turns an id into the selection it names: a symbol, module,
// group or file.
function selectionOf(id) {
  if (!id) return null;
  if (symbols.has(id)) return { type: "symbol", id };
  if (modules.has(id)) return { type: "module", id };
  if (groups.has(id)) return { type: "group", id };
  if (id.includes("|") && fileCell(id)) return { type: "file", id };
  return null;
}
// moduleOfSelection is the module a selection belongs to, if any.
function moduleOfSelection(sel) {
  if (!sel) return null;
  if (sel.type === "module") return sel.id;
  if (sel.type === "symbol") return (symbols.get(sel.id) || {}).module || null;
  if (sel.type === "file") return fileOf(sel.id).module;
  if (sel.type === "edge") return sel.from;
  return null;
}
// goToSelection moves the view to the selection, opening the groups that
// hide it: the one action, besides zooming and following Claude, that moves
// the map.
function goToSelection() {
  const sel = state.selected; if (!sel) return;
  if (state.mapTab !== "references") { goTo(sel); inspect(); return; }
  if (sel.type === "group") {
    const g = groups.get(sel.id); if (!g) return;
    const v = state.view; state.view = { ...v, x: g.x - v.w / 2, y: g.y - v.h / 2 }; applyView(); return;
  }
  goTo(sel.type === "symbol" ? sel.id : moduleOfSelection(sel));
  inspect();
}
// whereBlock tells the person where the selection is when they cannot see
// it, and offers to go there.
function whereBlock(box) {
  const sel = state.selected, moduleId = moduleOfSelection(sel), v = state.view, at = positionOf(sel);
  const hidden = state.mapTab === "references" && moduleId && anchorOf(moduleId) && anchorOf(moduleId).key !== moduleId;
  const off = at && v && (at.x < v.x || at.x > v.x + v.w || at.y < v.y || at.y > v.y + v.h);
  const row = h("div", { class: "where" });
  const go = h("button", { type: "button" }, "Go to"); go.addEventListener("click", goToSelection); row.appendChild(go);
  if (hidden) row.appendChild(h("span", {}, ` Inside the collapsed group ${(groups.get(anchorOf(moduleId).key) || {}).path || ""}/, which glows.`));
  else if (off) row.appendChild(h("span", {}, " Off screen."));
  box.appendChild(row);
}

function chip(id, parent) {
  const c = h("button", { class: "chip", type: "button" }, id.split(":").slice(-1)[0] || id);
  c.title = id; c.addEventListener("click", () => select(symbols.has(id) ? { type: "symbol", id } : { type: "module", id }));
  parent.appendChild(c);
}
// dl lists label and value rows, skipping empty values; a third entry is
// the label's tooltip, saying what the value measures.
function dl(parent, rows) { const d = h("dl"); for (const [k, v, tip] of rows) { if (v === undefined || v === "") continue; d.appendChild(h("dt", tip ? { title: tip } : {}, k)); d.appendChild(h("dd", tip ? { title: tip } : {}, v)); } parent.appendChild(d); }
// METRICS are a module's stability metrics: the key in the payload, the
// full name, the abbreviation the literature uses, and what it measures.
const METRICS = [
  ["ca", "Afferent coupling", "Ca", "How many other modules depend on this one. The higher it is, the more modules feel a change here."],
  ["ce", "Efferent coupling", "Ce", "How many other modules this one depends on. The higher it is, the more often changes elsewhere reach this module."],
  ["instability", "Instability", "I", "Ce ÷ (Ca + Ce), from 0 to 1. Near 0 the module is stable: others depend on it and it depends on little, so it is hard to change. Near 1 it is unstable: little depends on it, so it is free to change."],
  ["abstractness", "Abstractness", "A", "Interfaces ÷ top-level contracts, from 0 to 1. Near 1 the module's contract is mostly interfaces; near 0 it is concrete types, functions and values."],
  ["distance", "Distance from the main sequence", "D", "|A + I − 1|, from 0 to 1. Near 0 the module is balanced: stable modules are abstract and unstable ones concrete. Near 1 it is stable and concrete, so hard to change and hard to extend, or unstable and abstract, interfaces little depends on."],
];
// sliceBlock shows the slice an agent gets, collapsed. It opens only when
// the person opens it, and stays open while the selection stays the same,
// so a rebuild does not close it; the choice is not saved.
let sliceOpenFor = "";
function selectionKey(sel) { return sel ? `${sel.type}|${sel.id || sel.from + ">" + sel.to}` : ""; }
function sliceBlock(parent, slice) {
  if (!slice) return;
  const key = selectionKey(state.selected), box = h("details", { class: "slice" });
  box.open = sliceOpenFor === key;
  box.addEventListener("toggle", () => { sliceOpenFor = box.open ? key : ""; });
  const head = h("summary", { class: "slice-head" }); head.appendChild(h("h2", {}, "Agent context slice"));
  const text = JSON.stringify(slice, null, 2);
  const btn = h("button", { type: "button" }, "Copy");
  btn.addEventListener("click", ev => { ev.preventDefault(); navigator.clipboard.writeText(text).then(() => { btn.textContent = "Copied"; setTimeout(() => btn.textContent = "Copy", 1200); }, () => { btn.textContent = "Copy failed"; }); });
  head.appendChild(btn); box.appendChild(head); box.appendChild(h("pre", {}, text)); parent.appendChild(box);
}
// referenceTable lists an arrow's references as source, reference and
// destination, each row colored by how it changed since the base: added,
// removed, or changed when the destination's signature changed. Clicking a
// source or destination selects that symbol.
function referenceTable(e) {
  const keyOf = id => { const s = symbols.get(id), a = s && anchorOf(s.module); return a ? a.key : ""; };
  const gone = e.removed ? [] : removedLinks.filter(r => keyOf(r.from) === e.from && keyOf(r.to) === e.to);
  const rows = [...e.references.map(r => ({ r, change: e.removed ? "removed" : addedLinks.has(linkKey(r)) ? "added" : "" })), ...gone.map(r => ({ r, change: "removed" }))];
  for (const row of rows) if (!row.change && ["contract", "breaking"].includes((symbols.get(row.r.to) || {}).change)) row.change = "changed";
  const table = h("table", { class: "refs" }), head = h("tr");
  for (const label of ["Source", "Reference", "Destination"]) head.appendChild(h("th", {}, label));
  table.appendChild(h("thead")).appendChild(head);
  const body = table.appendChild(h("tbody"));
  const end = id => {
    const td = h("td"), name = id.split(":").pop();
    if (!symbols.has(id)) { td.appendChild(h("code", { title: id }, name)); return td; }
    const b = h("button", { type: "button", class: "link", title: id }, name);
    b.addEventListener("click", () => select({ type: "symbol", id })); td.appendChild(b); return td;
  };
  for (const { r, change } of rows) {
    const tr = h("tr", change ? { class: "ref-" + change, title: change } : {});
    tr.appendChild(end(r.from));
    tr.appendChild(h("td", { class: "ref-at" }, `${r.kind} at ${r.file}:${r.line}`));
    tr.appendChild(end(r.to)); body.appendChild(tr);
  }
  return { table, count: rows.length };
}
// diffBlock draws a line diff in full: removed lines red, added lines
// green, unchanged lines plain.
function diffBlock(lines) {
  const pre = h("pre", { class: "diff" });
  for (const d of lines) pre.appendChild(h("div", { class: d.op === "-" ? "sig-old" : d.op === "+" ? "sig-new" : "" }, d.op + " " + d.text));
  return pre;
}
// diffSummary counts a diff's added and removed lines.
function diffSummary(lines) {
  const added = lines.filter(d => d.op === "+").length, removed = lines.filter(d => d.op === "-").length;
  return `+${added} −${removed}`;
}
// showCode fills the Code tab with the selection's code: a symbol's lines,
// or its diff with the baseline when it changed, or a whole file.
function showCode() {
  const box = document.getElementById("code"); box.innerHTML = ""; const sel = state.selected;
  const note = text => box.appendChild(h("p", { class: "empty" }, text));
  if (!sel || !D) return note("Select a symbol or file to see its code.");
  if (sel.type === "symbol") {
    const s = symbols.get(sel.id); if (!s) return note("Select a symbol or file to see its code.");
    box.appendChild(h("h3", {}, s.name));
    if (s.file) box.appendChild(h("p", { class: "where" }, `${s.file}:${s.line}${s.end_line > s.line ? "–" + s.end_line : ""}`));
    // A changed symbol's code shows as a diff with the base: removed lines
    // red, added lines green, as its signature does.
    if (s.diff && s.diff.length) {
      box.appendChild(h("h2", {}, s.removed ? "Removed" : `Changes since the baseline · ${diffSummary(s.diff)}`)); box.appendChild(diffBlock(s.diff));
      return;
    }
    if (s.design) return note("Planned, not built: there is no code yet.");
    const code = codeOf(s);
    return code ? box.appendChild(h("pre", {}, code)) : note("This symbol is too long to show here, or its file could not be read. Select its file to see all of it.");
  }
  if (sel.type === "file") {
    const f = fileOf(sel.id), text = fileSource(f.file), diff = (D.file_diffs || {})[f.file];
    box.appendChild(h("h3", {}, f.file.split("/").pop()));
    box.appendChild(h("p", { class: "where" }, text ? `${f.file} · ${text.split("\n").length} lines` : f.file));
    // A file that differs from the base shows as a diff of the whole file,
    // as a changed symbol does.
    if (diff && diff.length) {
      const title = !text ? "Removed" : diff.every(d => d.op === "+") ? "Added since the baseline" : "Changes since the baseline";
      box.appendChild(h("h2", {}, `${title} · ${diffSummary(diff)}`)); box.appendChild(diffBlock(diff));
      return;
    }
    return text ? box.appendChild(h("pre", { class: "file" }, text)) : note("This file is too large, or could not be read, to show here.");
  }
  note("Select a symbol or file to see its code. Modules, groups and dependencies have no code of their own.");
}
function inspect() {
  showCode(); showTests();
  const box = document.getElementById("inspector"); box.innerHTML = ""; const sel = state.selected; if (!sel) return;
  whereBlock(box);
  if (sel.type === "symbol") {
    const s = symbols.get(sel.id); if (!s) return;
    box.appendChild(h("h3", {}, s.name));
    dl(box, [["Id", s.id], ["Kind", s.kind], ["Contract", s.contract ? "yes" : "internal"], ["File", s.file ? `${s.file}:${s.line}` : "not built"], ["Change", s.change || (s.design ? "planned, not built" : "unchanged")]]);
    const sig = h("pre");
    if (s.removed) sig.appendChild(h("div", { class: "sig-old" }, "- " + s.signature));
    else if (s.before) { sig.appendChild(h("div", { class: "sig-old" }, "- " + s.before)); sig.appendChild(h("div", { class: "sig-new" }, "+ " + s.signature)); }
    else sig.textContent = s.signature;
    box.appendChild(h("h2", {}, "Signature")); box.appendChild(sig);
    const uses = links.filter(l => l.from === s.id).map(l => l.to), callers = links.filter(l => l.to === s.id).map(l => l.from);
    const usedBefore = removedLinks.filter(l => l.from === s.id).map(l => l.to), calledBefore = removedLinks.filter(l => l.to === s.id).map(l => l.from);
    const chipList = (title, ids) => { if (!ids.length) return; box.appendChild(h("h2", {}, title)); const c = h("div", { class: "chips" }); [...new Set(ids)].forEach(id => chip(id, c)); box.appendChild(c); };
    chipList("Uses", uses); chipList("Callers", callers);
    chipList("No longer uses", usedBefore); chipList("No longer called by", calledBefore);
    if (s.doc) { box.appendChild(h("h2", {}, "Documentation")); box.appendChild(h("pre", { class: "doc" }, s.doc)); }
    sliceBlock(box, s.slice);
  } else if (sel.type === "file") {
    const f = fileOf(sel.id), cell = fileCell(sel.id), m = modules.get(f.module); if (!cell || !m) return;
    if (cell.manifest) {
      box.appendChild(h("h3", {}, f.file.split("/").pop()));
      dl(box, [["Path", f.file], ["Module", m.label || m.path]]);
      box.appendChild(h("p", {}, "The Go module begins here: packages in this directory and beneath it, down to the next go.mod, are imported through the module path it declares."));
      const moduleChip = h("div", { class: "chips" }); chip(m.id, moduleChip); box.appendChild(moduleChip);
      return;
    }
    const own = (byModule.get(f.module) || []).filter(s => s.file === f.file);
    const contracts = own.filter(s => s.contract), internal = own.filter(s => !s.contract), changed = own.filter(s => s.change);
    box.appendChild(h("h3", {}, f.file.split("/").pop()));
    dl(box, [["Path", f.file], ["Module", m.label || m.path],
      ["Symbols", String(own.length), "Symbols: every declaration in this file, its contracts and its internals together."],
      ["Contracts", String(contracts.length), "Contracts: the declarations other modules may use, such as exported or public names."],
      ["Internal", String(internal.length), "Internal: the declarations only this module may use."],
      ["Changed", changed.length ? String(changed.length) : "", "Changed: the declarations that differ from the baseline, added, removed, moved, or changed in signature or code."]]);
    const moduleChip = h("div", { class: "chips" }); chip(m.id, moduleChip); box.appendChild(moduleChip);
    const ids = new Set(own.map(s => s.id)), uses = new Set(), usedBy = new Set();
    for (const l of links) {
      const a = symbols.get(l.from), b = symbols.get(l.to); if (!a || !b) continue;
      const name = x => x.file && x.module === f.module ? x.file : (modules.get(x.module) || {}).label || x.module;
      if (ids.has(l.from) && !ids.has(l.to)) uses.add(name(b));
      if (ids.has(l.to) && !ids.has(l.from)) usedBy.add(name(a));
    }
    const list = (title, values) => { if (!values.size) return; box.appendChild(h("h2", {}, `${title} (${values.size})`)); const ul = h("ul"); [...values].sort().forEach(v => ul.appendChild(h("li", {}, v))); box.appendChild(ul); };
    list("Uses", uses); list("Used by", usedBy);
    if (contracts.length) { box.appendChild(h("h2", {}, `Contracts (${contracts.length})`)); const c = h("div", { class: "chips" }); contracts.forEach(s => chip(s.id, c)); box.appendChild(c); }
    if (internal.length) { box.appendChild(h("h2", {}, `Internal (${internal.length})`)); const c = h("div", { class: "chips" }); internal.forEach(s => chip(s.id, c)); box.appendChild(c); }
    sliceBlock(box, (D.file_slices || {})[f.file]);
  } else if (sel.type === "module") {
    const m = modules.get(sel.id); if (!m) return;
    box.appendChild(h("h3", {}, m.label || m.path));
    dl(box, [["Id", m.id], ["Manifest", m.manifest || ""], ["Layer", m.layer + (m.side ? ` (${m.side})` : "")], [D.architecture === "modular" ? "Context" : "Slice", m.section || ""], ["API", m.public ? "public" : ""], ["Status", m.design ? "planned, not built" : m.new ? "new" : m.removed ? "removed" : ""]]);
    box.appendChild(h("p", {}, m.guidance));
    if (state.moved.has(m.id)) {
      const moved = h("p", {}, "You placed this module here. "), back = h("button", { type: "button" }, "Put it back");
      back.addEventListener("click", () => savePosition(m.id, null));
      moved.appendChild(back); box.appendChild(moved);
    } else if (LIVE && !m.design) box.appendChild(h("p", { class: "hint" }, LAYERED_STYLES.includes(D.architecture) ? "Long-press the module to pick it up: drop it elsewhere in its band to place it, or in another layer to move it there in treaty.yaml." : "Long-press the module to pick it up and place it elsewhere in its area."));
    const t = h("table", { class: "metrics" }); t.innerHTML = "<tr><th>Metric</th><th>Before</th><th>After</th></tr>";
    for (const [key, name, abbr, about] of METRICS) {
      const tip = `${name} (${abbr}): ${about}`, row = h("tr", { title: tip });
      row.appendChild(h("td", { class: "metric-name" }, `${name} (${abbr})`)); row.appendChild(h("td", {}, m.before ? String(m.before[key]) : "—")); row.appendChild(h("td", {}, String(m.metrics[key]))); t.appendChild(row);
    }
    box.appendChild(t);
    if (m.files && m.files.length) {
      box.appendChild(h("h2", {}, "Files")); const ul = h("ul");
      (m.manifest ? [m.manifest, ...m.files] : m.files).forEach(f => { const key = fileKey(m.id, f), li = h("li", fileCell(key) ? { class: "link", tabindex: 0, role: "button" } : {}, f); if (fileCell(key)) { li.addEventListener("click", () => select({ type: "file", id: key })); li.addEventListener("keydown", ev => { if (ev_is(ev)) { ev.preventDefault(); select({ type: "file", id: key }); } }); } ul.appendChild(li); });
      box.appendChild(ul);
    }
    const contracts = (byModule.get(m.id) || []).filter(s => s.contract);
    if (contracts.length) { box.appendChild(h("h2", {}, `Contracts (${contracts.length})`)); const c = h("div", { class: "chips" }); contracts.forEach(s => chip(s.id, c)); box.appendChild(c); }
    sliceBlock(box, m.slice);
  } else if (sel.type === "edge") {
    const e = sel.edge;
    const end = key => { const g = groups.get(key), m = modules.get(key); return g ? { label: g.path + "/", layer: g.layer } : { label: m.label || m.path, layer: m.layer }; };
    const a = end(e.from), b = end(e.to);
    box.appendChild(h("h3", {}, `${shortName(a.label)} → ${shortName(b.label)}`));
    const p = h("p");
    if (e.removed) p.appendChild(document.createTextNode("Removed: the base had this dependency and the working tree no longer does. Its references are as they were in the base."));
    else {
      p.appendChild(h("span", { class: "pill " + (e.violation ? "fail" : "pass") }, e.violation ? "Violation" : "Allowed"));
      p.appendChild(document.createTextNode(` ${a.layer} → ${b.layer}: ${e.violation ? e.rule || "breaks the architecture's rules" : "allowed by the architecture's rules"}.`));
    }
    box.appendChild(p);
    const refs = referenceTable(e);
    box.appendChild(h("h2", {}, `References (${refs.count})`));
    if (refs.count) box.appendChild(refs.table);
    if (e.imports && e.imports.length) {
      box.appendChild(h("h2", {}, `Imports (${e.imports.length})`));
      const il = h("ul"); for (const i of e.imports) { const li = h("li"); li.appendChild(h("code", {}, i.to)); li.appendChild(document.createTextNode(` imported at ${i.file}:${i.line}`)); il.appendChild(li); } box.appendChild(il);
    }
    const slice = { schema: "treaty/slice/v1", violation: e.violation ? { rule: e.rule || `${a.layer} may not depend on ${b.layer}`, from: e.from, to: e.to, references: e.references, imports: e.imports } : null };
    if (e.violation) sliceBlock(box, slice);
  } else if (sel.type === "group") {
    const g = groups.get(sel.id); if (!g) return;
    const collapsed = isCollapsed(g);
    box.appendChild(h("h3", {}, g.path + "/"));
    const contracts = g.modules.reduce((n, id) => n + (byModule.get(id) || []).filter(s => s.contract).length, 0);
    const findings = D.findings.filter(f => f.targets.some(t => g.modules.includes(t) || g.modules.some(id => t.startsWith(id + ":")))).length;
    dl(box, [["Layer", g.layer], ["Modules", String(g.modules.length)], ["Contracts", String(contracts)], ["Findings", String(findings)], ["Shown", collapsed ? "collapsed" : "expanded"]]);
    const toggle = h("button", { type: "button" }, collapsed ? "Expand" : "Collapse");
    toggle.addEventListener("click", () => toggleGroup(g.id));
    box.appendChild(toggle);
    box.appendChild(h("p", { class: "empty" }, "A group is a directory, drawn for orientation only. Layer rules and metrics apply to each module inside it."));
    box.appendChild(h("h2", {}, "Modules"));
    const c = h("div", { class: "chips" }); g.modules.forEach(id => chip(id, c)); box.appendChild(c);
  }
}
function highlightQueue() {
  document.querySelectorAll(".queue-item").forEach(node => {
    const targets = JSON.parse(node.dataset.targets); const sel = state.selected;
    node.classList.toggle("active", !!sel && (targets.includes(sel.id) || (sel.type === "edge" && targets.includes(sel.from) && targets.includes(sel.to))));
  });
}
function buildQueue() {
  const q = document.getElementById("queue"); q.innerHTML = "";
  if (!D.findings.length) { q.appendChild(h("p", { class: "empty" }, "Nothing to review.")); return; }
  for (const f of D.findings) {
    const item = h("div", { class: "queue-item", tabindex: 0, role: "button" }); item.dataset.targets = JSON.stringify(f.targets);
    item.appendChild(h("span", { class: "sev " + f.severity, "aria-label": f.severity }, f.severity === "high" ? "!" : ""));
    const body = h("div"); body.appendChild(h("div", { class: "title" }, f.title)); body.appendChild(h("div", { class: "detail" }, f.detail)); item.appendChild(body);
    const pick = () => {
      const t = f.targets;
      if (f.kind === "layer_violation") { const e = edges.find(x => x.from === t[0] && x.to === t[1]); if (e) return select({ type: "edge", from: e.from, to: e.to, edge: e }); }
      if (symbols.has(t[0])) return select({ type: "symbol", id: t[0] });
      if (modules.has(t[0])) return select({ type: "module", id: t[0] });
    };
    item.addEventListener("click", pick); item.addEventListener("keydown", ev => { if (ev_is(ev)) { ev.preventDefault(); pick(); } });
    q.appendChild(item);
  }
}
