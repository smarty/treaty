// The Tests tab and test coverage: every test by package, or the tests for
// the selection, with their outcomes; running them; and the coverage rings
// drawn around icons on the map and in the tab.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// testState holds the server's latest test report. The page fetches it after
// every rebuild and whenever the server's test version changes. open and
// details remember which tree rows the person opened or closed.
const testState = { report: null, shown: -1, loading: false, pending: false, error: "", lines: new Map(), coverageKey: "", open: new Map(), details: new Map() };
const TEST_GLYPHS = { pass: "✓", fail: "✗", skip: "–", running: "●", queued: "◌" };
const TEST_WORDS = { pass: "passed", fail: "failed", skip: "skipped", running: "running", queued: "queued" };

// ---- Coverage ----
// Coverage is per line: the server sends, for each file, the lines that
// hold a statement some run executed and those none did. A symbol's
// coverage counts the lines between its first and last; a file's, module's
// or directory's counts all of theirs. Nothing that has not run has
// coverage, and neither does code with no statements, such as a type.
function ingestTests(report) {
  report.tests = report.tests || []; report.results = report.results || {}; report.coverage = report.coverage || {};
  testState.report = report;
  testState.lines = new Map(Object.entries(report.coverage));
  const key = JSON.stringify(report.coverage), changed = key !== testState.coverageKey;
  testState.coverageKey = key;
  return changed;
}
// countIn counts the values of a sorted list from from to to, inclusive.
function countIn(sorted, from, to) {
  const below = v => { let lo = 0, hi = sorted.length; while (lo < hi) { const mid = (lo + hi) >> 1; if (sorted[mid] < v) lo = mid + 1; else hi = mid; } return lo; };
  return below(to + 1) - below(from);
}
function coverageIn(file, from = 1, to = Infinity) {
  const c = file && testState.lines.get(file); if (!c) return null;
  const hit = countIn(c.covered || [], from, to), miss = countIn(c.uncovered || [], from, to);
  return hit + miss ? { hit, miss } : null;
}
function addCoverage(a, b) { return !a ? b : !b ? a : { hit: a.hit + b.hit, miss: a.miss + b.miss }; }
// A type's coverage includes its members', so a Go type, which holds no
// statements itself, shows how well its methods are covered.
function symbolCoverage(s) {
  if (!s || !s.file || !s.line || s.removed || s.design) return null;
  const own = coverageIn(s.file, s.line, Math.max(s.end_line || s.line, s.line));
  return (members.get(s.id) || []).reduce((sum, m) => addCoverage(sum, m.id === s.id ? null : symbolCoverage(m)), own);
}
function moduleCoverage(m) { return m ? (m.files || []).reduce((sum, f) => addCoverage(sum, coverageIn(f)), null) : null; }
function percent(cov) { const f = cov.hit / (cov.hit + cov.miss); return f >= 1 ? 100 : Math.floor(f * 100); }
function coverageWords(cov) { return cov ? `${percent(cov)}% covered, ${cov.hit} of ${cov.hit + cov.miss} lines` : ""; }
// coverageRing draws coverage as a ring around an icon: a faint track, and
// over it an arc from 12 o'clock clockwise, so half covered is the right
// half. Nothing is drawn for code no run has covered.
function coverageRing(parent, x, y, r, cov, width) {
  if (!cov) return null;
  const f = cov.hit / (cov.hit + cov.miss), g = el("g", { class: "coverage-ring", "pointer-events": "none" }, parent);
  const common = { fill: "none", "stroke-width": width };
  el("circle", { ...common, cx: x, cy: y, r, stroke: color("--hex-stroke") }, g);
  if (f >= 1) el("circle", { ...common, cx: x, cy: y, r, stroke: color("--ink") }, g);
  else if (f > 0) {
    const a = 2 * Math.PI * f;
    el("path", { ...common, stroke: color("--ink"), d: `M ${x} ${y - r} A ${r} ${r} 0 ${f > 0.5 ? 1 : 0} 1 ${x + r * Math.sin(a)} ${y - r * Math.cos(a)}` }, g);
  }
  return g;
}

// ---- Fetching and running ----
async function refreshTests() {
  if (!LIVE || !D) return;
  if (testState.loading) { testState.pending = true; return; }
  testState.loading = true;
  try {
    const response = await fetch("api/tests", { cache: "no-store" });
    if (!response.ok) throw new Error((await response.text()).trim());
    testState.error = "";
    applyTests(await response.json());
  } catch (err) {
    testState.error = `could not load the tests: ${err.message}`; showTests();
  } finally {
    testState.loading = false;
    if (testState.pending) { testState.pending = false; refreshTests(); }
  }
}
// applyTests shows a report, redrawing the map only when coverage changed.
function applyTests(report) {
  testState.shown = report.version;
  if (ingestTests(report) && D) render();
  showTests();
}
async function postTests(path, body) {
  try {
    const response = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!response.ok) throw new Error((await response.text()).trim());
    testState.error = "";
    if (response.status !== 204) applyTests(await response.json());
  } catch (err) {
    testState.error = err.message; showTests();
  }
}
function runTests(ids) { if (ids.length) postTests("api/tests/run", { ids }); }
function stopTests() { postTests("api/tests/stop", {}); }

// ---- The Tests tab ----
function testStatus(id) { const r = testState.report.results[id]; return r ? r.status : ""; }
// testScope is which tests the tab shows: all of them with nothing
// selected, else those for the selection. A symbol's tests use it or one of
// its members; a file's use one of its symbols or sit in its _test file; a
// module's or group's sit in it or use one of its symbols.
function testScope() {
  const sel = state.selected, usesAny = ids => t => (t.targets || []).some(id => ids.has(id));
  if (!sel) return { all: true, match: () => true };
  if (sel.type === "symbol") {
    const s = symbols.get(sel.id); if (!s) return { all: true, match: () => true };
    const ids = new Set([s.id, ...(members.get(s.id) || []).map(m => m.id)]);
    return { title: s.name, match: usesAny(ids), header: box => symbolCard(box, s) };
  }
  if (sel.type === "file") {
    const f = fileOf(sel.id), ids = new Set((byModule.get(f.module) || []).filter(s => s.file === f.file).map(s => s.id));
    const sibling = f.file.replace(/(\.[^./]+)$/, "_test$1"), uses = usesAny(ids);
    return { title: f.file.split("/").pop(), match: t => t.file === sibling || uses(t), header: box => fileCard(box, f) };
  }
  if (sel.type === "module" || sel.type === "group") {
    const ids = new Set(sel.type === "module" ? [sel.id] : (groups.get(sel.id) || { modules: [] }).modules);
    const m = modules.get(sel.id), g = groups.get(sel.id);
    return { title: m ? m.label || m.path : g ? g.path + "/" : sel.id, match: t => ids.has(t.module) || (t.targets || []).some(id => ids.has((symbols.get(id) || {}).module)), header: box => modulesCard(box, [...ids]) };
  }
  if (sel.type === "edge") {
    const ids = new Set(sel.edge.references.flatMap(r => [r.from, r.to]));
    return { title: "this dependency", match: usesAny(ids) };
  }
  return { all: true, match: () => true };
}
// testIcon draws a small icon with its coverage ring.
function testIcon(draw, cov) {
  const svg = el("svg", { width: 18, height: 18, viewBox: "-9 -9 18 18", "aria-hidden": "true", class: "test-icon" });
  draw(svg);
  coverageRing(svg, 0, 0, 7.6, cov, 1.3);
  return svg;
}
function hexIcon(svg, fill, stroke, dashed) {
  const hex = el("polygon", { points: hexPoints(0, 0, 5.4).map(q => q.join(",")).join(" "), fill, stroke, "stroke-width": 1.3 }, svg);
  if (dashed) hex.setAttribute("stroke-dasharray", "2 1.5");
}
function moduleIcon(m, cov) { return testIcon(svg => hexIcon(svg, m ? color("--module") : "none", m ? color("--module-stroke") : color("--hex-stroke"), !m), cov); }
function fileIcon(cov) { return testIcon(svg => { hexIcon(svg, color("--accent"), color("--hex-stroke"), false); svg.lastChild.setAttribute("fill-opacity", "0.2"); }, cov); }
function symbolIcon(s, cov) { return testIcon(svg => drawShape(svg, s.kind, 0, 0, 4.2, s.change, s.design), cov); }
// coverageRow is one line naming something with its coverage; it selects
// that thing when it has a selection.
function coverageRow(parent, icon, name, cov, sel, title) {
  const row = h(sel ? "button" : "div", sel ? { type: "button", class: "coverage-row link", title: title || name } : { class: "coverage-row", title: title || name });
  row.appendChild(icon);
  row.appendChild(h("span", { class: "tree-name" }, name));
  row.appendChild(h("span", { class: "coverage-text" }, cov ? `${percent(cov)}%` : "—"));
  if (cov) row.title += ` · ${coverageWords(cov)}`;
  if (sel) row.addEventListener("click", () => select(sel));
  parent.appendChild(row);
  return row;
}
function coverageHead(box, icon, name, cov, about) {
  const head = h("div", { class: "coverage-head" });
  head.appendChild(icon);
  const text = h("div");
  text.appendChild(h("h3", {}, name));
  text.appendChild(h("p", { class: "where" }, cov ? coverageWords(cov) : about));
  head.appendChild(text);
  box.appendChild(head);
}
function symbolCard(box, s) {
  const cov = symbolCoverage(s);
  coverageHead(box, symbolIcon(s, cov), s.name, cov, s.design ? "Planned, not built." : "No coverage: its tests have not run on this code, or it holds no statements.");
}
function fileCard(box, f) {
  const cov = coverageIn(f.file);
  coverageHead(box, fileIcon(cov), f.file.split("/").pop(), cov, "No coverage: no run has covered this file as it is now.");
  const own = (byModule.get(f.module) || []).filter(s => s.file === f.file && symbolCoverage(s)).sort(symbolOrder);
  if (!own.length) return;
  const list = h("div", { class: "coverage-list" });
  for (const s of own) coverageRow(list, symbolIcon(s, symbolCoverage(s)), s.name, symbolCoverage(s), { type: "symbol", id: s.id }, s.signature);
  box.appendChild(list);
}
function modulesCard(box, ids) {
  const shown = ids.map(id => modules.get(id)).filter(Boolean);
  if (shown.length === 1) {
    const m = shown[0], cov = moduleCoverage(m);
    coverageHead(box, moduleIcon(m, cov), m.label || m.path, cov, "No coverage: no run has covered this package as it is now.");
    const list = h("div", { class: "coverage-list" });
    for (const file of m.files || []) { const c = coverageIn(file); if (c) coverageRow(list, fileIcon(c), file.split("/").pop(), c, { type: "file", id: fileKey(m.id, file) }, file); }
    if (list.childElementCount) box.appendChild(list);
    return;
  }
  const g = groups.get(state.selected.id), cov = shown.reduce((sum, m) => addCoverage(sum, moduleCoverage(m)), null);
  coverageHead(box, moduleIcon(null, cov), g ? g.path + "/" : "", cov, "No coverage: no run has covered these packages as they are now.");
  const list = h("div", { class: "coverage-list" });
  for (const m of shown) coverageRow(list, moduleIcon(m, moduleCoverage(m)), moduleLabel(m), moduleCoverage(m), { type: "module", id: m.id }, m.path);
  box.appendChild(list);
}
// tally counts tests by outcome, for a summary such as "3 passed · 1 failed".
function tally(tests) {
  const counts = {};
  for (const t of tests) { const status = testStatus(t.id) || "none"; counts[status] = (counts[status] || 0) + 1; }
  return counts;
}
function tallyText(counts) {
  const parts = ["fail", "pass", "skip", "running", "queued"].filter(k => counts[k]).map(k => `${counts[k]} ${TEST_WORDS[k]}`);
  if (counts.none) parts.push(`${counts.none} not run`);
  return parts.join(" · ");
}
function showTests() {
  const box = document.getElementById("tests"); if (!box) return;
  box.innerHTML = "";
  const note = text => box.appendChild(h("p", { class: "empty" }, text));
  if (!LIVE) return note("Tests run on the live map. Start it with treaty serve and open the map it prints.");
  const report = testState.report;
  if (!report || !D) return note(testState.error || "Loading the tests…");
  const scope = testScope(), visible = report.tests.filter(scope.match);
  const bar = h("div", { class: "tests-bar" });
  const run = h("button", { type: "button", title: scope.all ? "Run every test" : `Run the tests shown, those for ${scope.title}` }, `▶ Run ${scope.all ? "all" : "shown"} (${visible.length})`);
  run.disabled = report.running || !visible.length;
  run.addEventListener("click", () => runTests(visible.map(t => t.id)));
  bar.appendChild(run);
  if (report.running) { const stop = h("button", { type: "button", title: "Stop the run" }, "■ Stop"); stop.addEventListener("click", stopTests); bar.appendChild(stop); }
  bar.appendChild(h("span", { class: "tests-summary", role: "status" }, tallyText(tally(visible))));
  box.appendChild(bar);
  for (const text of [testState.error, report.error]) if (text) box.appendChild(h("p", { class: "tests-error" }, text));
  if (scope.header) scope.header(box);
  if (!scope.all) box.appendChild(h("h2", {}, `Tests for ${scope.title}`));
  if (!visible.length) return note(scope.all ? "No tests found. Treaty runs Go tests: the Test and Fuzz functions of _test.go files." : `No test uses ${scope.title}.`);
  box.appendChild(testTree(visible, !scope.all));
}
// testTree lays the tests out like a file tree: directories, then each
// package's tests, then the subtests its runs reported.
function testTree(visible, expand) {
  const root = { path: ".", name: D.root || "repository", dirs: new Map(), module: null, tests: [] };
  const node = path => {
    if (path === "." || path === "") return root;
    let at = root;
    path.split("/").forEach((part, i, parts) => {
      const sub = parts.slice(0, i + 1).join("/");
      if (!at.dirs.has(part)) at.dirs.set(part, { path: sub, name: part, dirs: new Map(), module: null, tests: [] });
      at = at.dirs.get(part);
    });
    return at;
  };
  for (const t of visible) { const m = modules.get(t.module), n = node(m ? m.path : t.module.slice(t.module.indexOf(":") + 1)); n.module = n.module || t.module; n.tests.push(t); }
  const tree = h("div", { class: "test-tree", role: "tree", "aria-label": "Tests by package" });
  const beneath = n => [...n.tests, ...[...n.dirs.values()].flatMap(beneath)];
  const dirRow = (n, depth) => {
    const key = "dir:" + n.path, all = beneath(n), m = modules.get(n.module);
    const open = testState.open.has(key) ? testState.open.get(key) : expand || !m;
    const cov = m ? moduleCoverage(m) : [...modules.values()].filter(x => x.path === n.path || x.path.startsWith(n.path + "/")).reduce((sum, x) => addCoverage(sum, moduleCoverage(x)), null);
    const counts = tally(all), title = `${n.path}${m ? " · package " + (m.name || m.path) : " · directory"}${cov ? " · " + coverageWords(cov) : ""}`;
    row(depth, open, () => testState.open.set(key, !open), m ? moduleIcon(m, cov) : moduleIcon(null, cov), n.name, title, counts.fail ? `✗${counts.fail}` : "", cov ? `${percent(cov)}%` : "", all.map(t => t.id), counts.fail ? "fail" : "");
    if (open) children(n, depth + 1);
  };
  const children = (n, depth) => {
    for (const t of [...n.tests].sort((a, b) => a.line - b.line || (a.name < b.name ? -1 : 1))) testRow(t.id, t.name, t, depth);
    for (const d of [...n.dirs.values()].sort((a, b) => a.name < b.name ? -1 : 1)) dirRow(d, depth);
  };
  // testRow draws a test and, when open, its details and its subtests.
  const testRow = (id, name, t, depth) => {
    const result = testState.report.results[id], status = result ? result.status : "";
    const subs = Object.keys(testState.report.results).filter(k => k.startsWith(id + "/") && !k.slice(id.length + 1).includes("/")).sort();
    const key = "test:" + id, open = testState.details.has(key) ? testState.details.get(key) : status === "fail" && !subs.some(k => testStatus(k) === "fail");
    const glyph = h("span", { class: "test-status " + (status || "none"), "aria-hidden": "true" }, TEST_GLYPHS[status] || "○");
    const title = `${name}${t ? ` · ${t.file}:${t.line}` : ""} · ${TEST_WORDS[status] || "not run yet"}${result && result.elapsed ? ` in ${result.elapsed.toFixed(2)}s` : ""}`;
    row(depth, open, () => testState.details.set(key, !open), glyph, name, title, result && result.elapsed ? `${result.elapsed.toFixed(2)}s` : "", "", [id], status, `${name}, ${TEST_WORDS[status] || "not run yet"}`);
    if (!open) return;
    const more = h("div", { class: "test-details" }); more.style.paddingLeft = `${(depth + 1) * 14 + 6}px`;
    if (t) more.appendChild(h("p", { class: "where" }, `${t.file}:${t.line}`));
    if (t && t.targets && t.targets.length) { const c = h("div", { class: "chips" }); t.targets.filter(x => symbols.has(x)).forEach(x => chip(x, c)); if (c.childElementCount) { more.appendChild(h("h2", {}, "Uses")); more.appendChild(c); } }
    if (result && result.output) more.appendChild(h("pre", { class: "test-output" + (status === "fail" ? " failed" : "") }, result.output));
    if (more.childElementCount) tree.appendChild(more);
    for (const sub of subs) testRow(sub, sub.slice(id.length + 1), null, depth + 1);
  };
  // row draws one line of the tree: a toggle with its icon and name, and a
  // button that runs the tests beneath it.
  const row = (depth, open, toggle, icon, name, title, badge, cov, ids, status, label) => {
    const line = h("div", { class: "test-row" + (status === "fail" ? " failed" : ""), role: "treeitem", "aria-expanded": String(open) });
    const button = h("button", { type: "button", class: "test-toggle", title, "aria-label": label || name });
    button.style.paddingLeft = `${depth * 14 + 4}px`;
    button.appendChild(h("span", { class: "tree-arrow", "aria-hidden": "true" }, open ? "▾" : "▸"));
    button.appendChild(icon);
    button.appendChild(h("span", { class: "tree-name" }, name));
    if (badge) button.appendChild(h("span", { class: "tree-count" + (badge.startsWith("✗") ? " fail" : "") }, badge));
    if (cov) button.appendChild(h("span", { class: "coverage-text" }, cov));
    button.addEventListener("click", () => { toggle(); showTests(); const again = document.querySelector(`#tests [data-row="${CSS.escape(ids[0] + "|" + depth)}"] .test-toggle`); if (again) again.focus(); });
    line.dataset.row = ids[0] + "|" + depth;
    const play = h("button", { type: "button", class: "test-run", title: ids.length === 1 ? `Run ${name}` : `Run the ${ids.length} tests in ${name}`, "aria-label": `Run ${name}` }, "▶");
    play.disabled = testState.report.running;
    play.addEventListener("click", () => runTests(ids));
    line.append(button, play);
    tree.appendChild(line);
  };
  children(root, 0);
  return tree;
}
