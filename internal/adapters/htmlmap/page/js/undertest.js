// Coverage: a tab per opening, in the center by default, showing the
// code some tests ran.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// ---- Code under test ----
// Each tab shows, file by file, the functions and methods the tests reached
// and those of the contracts they use. A line with probes reads by a glyph,
// a color and a count, so it never relies on color alone: ✓ every probe
// reached, ◐ some, ✗ none, with reached of total and the percentage on the
// right. Clicking a line with some reached lists what was and wasn't.
const UNDER_TEST_MARKS = { full: "✓", part: "◐", none: "✗" };
const UNDER_TEST_WORDS = { full: "every probe reached", part: "some probes reached", none: "no probe reached" };
let underTestTabs = 0;
// openCodeUnderTest opens a new tab on the code some tests ran.
function openCodeUnderTest(ids, title) {
  if (!LIVE || !ids.length) return;
  const id = "undertest-" + (++underTestTabs);
  const node = h("section", { class: "panel undertest-panel", "data-panel": id, "aria-label": "Coverage: " + title });
  const body = h("div", { class: "undertest" });
  node.appendChild(body);
  document.getElementById("panels").appendChild(node);
  PANELS[id] = { title: "Coverage · " + title, closable: true };
  dock.center.tabs.push(id);
  dock.center.active = id;
  renderLayout();
  loadCodeUnderTest(body, ids, title);
}
// testCodeID names the test whose run covers a test or subtest: a subtest
// runs inside its top-level test, which records what both reached.
function testCodeID(id) {
  const slash = id.indexOf("/", id.indexOf("#"));
  return slash < 0 ? id : id.slice(0, slash);
}
async function loadCodeUnderTest(body, ids, title) {
  body.innerHTML = "";
  body.appendChild(h("p", { class: "empty" }, "Loading the code these tests ran…"));
  try {
    const response = await fetch("api/tests/code", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ids: [...new Set(ids.map(testCodeID))] }) });
    if (!response.ok) throw new Error((await response.text()).trim());
    showCodeUnderTest(body, await response.json(), ids, title);
  } catch (err) {
    body.innerHTML = "";
    body.appendChild(h("p", { class: "tests-error" }, err.message));
  }
}
// lineState reads a line's probes as full, part or none.
function lineState(line) { return line.reached === line.probes.length ? "full" : line.reached ? "part" : "none"; }
function percentOf(reached, total) { return total ? Math.floor(100 * reached / total) : 0; }
function showCodeUnderTest(body, code, ids, title) {
  body.innerHTML = "";
  const bar = h("div", { class: "tests-bar" });
  const refresh = h("button", { type: "button", title: "Show what the latest runs reached" }, "↻ Refresh");
  refresh.addEventListener("click", () => loadCodeUnderTest(body, ids, title));
  const run = h("button", { type: "button", title: `Run ${ids.length === 1 ? "this test" : `these ${ids.length} tests`}, then refresh` }, `▶ Run (${ids.length})`);
  run.disabled = !!(testState.report && testState.report.running);
  run.addEventListener("click", () => runTests(ids));
  bar.append(run, refresh);
  body.appendChild(h("h3", {}, "Code under test"));
  const names = ids.map(id => id.slice(id.indexOf("#") + 1));
  body.appendChild(h("p", { class: "where" }, ids.length === 1 ? names[0] : `${ids.length} tests in ${title}: ${names.slice(0, 5).join(", ")}${names.length > 5 ? ", …" : ""}`));
  body.appendChild(bar);
  if (!code.ran || !code.ran.length) {
    body.appendChild(h("p", { class: "empty" }, `${ids.length === 1 ? "This test hasn't" : "None of these tests has"} run since its code last changed, so there is nothing to show yet. Run ${ids.length === 1 ? "it" : "them"}, then refresh.`));
    return;
  }
  const probes = code.files.reduce((sum, f) => sum + f.probes, 0), reached = code.files.reduce((sum, f) => sum + f.reached, 0);
  body.appendChild(h("p", { class: "undertest-total" }, `${code.ran.length} of ${ids.length} test${ids.length === 1 ? "" : "s"} ran · probes reached ${reached} of ${probes} · ${percentOf(reached, probes)}%`));
  const legend = h("p", { class: "undertest-legend" });
  for (const state of ["full", "part", "none"]) legend.appendChild(h("span", { class: "ut-key " + state }, `${UNDER_TEST_MARKS[state]} ${UNDER_TEST_WORDS[state]}`));
  body.appendChild(legend);
  if (!code.files.length) { body.appendChild(h("p", { class: "empty" }, "The tests reached no instrumented code.")); return; }
  for (const file of code.files) body.appendChild(fileUnderTest(file));
}
// fileUnderTest draws one file: its shown spans of lines, the gaps between
// them folded, every line with probes marked.
function fileUnderTest(file) {
  const box = h("section", { class: "ut-file" });
  const head = h("div", { class: "ut-file-head" });
  head.append(h("span", { class: "ut-path" }, file.file), h("span", { class: "ut-count" }, `${file.reached}/${file.probes} · ${percentOf(file.reached, file.probes)}%`));
  box.appendChild(head);
  const text = fileSource(file.file).split("\n"), byLine = new Map(file.lines.map(l => [l.line, l]));
  const table = h("table", { class: "ut-code" }), rows = h("tbody");
  table.appendChild(rows);
  let last = 0;
  for (const [from, to] of file.ranges) {
    if (last && from > last + 1) { const gap = h("tr", { class: "ut-gap" }), cell = h("td", { colspan: 4 }, `⋯ ${from - last - 1} line${from - last - 1 === 1 ? "" : "s"}`); gap.appendChild(cell); rows.appendChild(gap); }
    for (let n = from; n <= to; n++) rows.appendChild(lineUnderTest(n, text[n - 1] || "", byLine.get(n)));
    last = to;
  }
  box.appendChild(table);
  return box;
}
function lineUnderTest(n, source, line) {
  const state = line ? lineState(line) : "", row = h("tr", { class: "ut-line" + (state ? " " + state : "") });
  row.append(h("td", { class: "ln" }, String(n)), h("td", { class: "mark", "aria-hidden": "true" }, UNDER_TEST_MARKS[state] || ""), h("td", { class: "src" }, source));
  row.appendChild(h("td", { class: "cov" }, line ? `${line.reached}/${line.probes.length} · ${percentOf(line.reached, line.probes.length)}%` : ""));
  if (line) row.title = `Line ${n}: ${line.reached} of ${line.probes.length} probes reached, ${percentOf(line.reached, line.probes.length)}%, ${UNDER_TEST_WORDS[state]}${state === "part" ? ". Click for what was and wasn't." : ""}`;
  if (state !== "part") return row;
  row.tabIndex = 0;
  row.setAttribute("role", "button");
  row.setAttribute("aria-expanded", "false");
  const toggle = () => {
    const open = row.nextElementSibling && row.nextElementSibling.classList.contains("ut-detail");
    if (open) row.nextElementSibling.remove(); else row.after(probeDetails(line, source));
    row.setAttribute("aria-expanded", String(!open));
  };
  row.addEventListener("click", toggle);
  row.addEventListener("keydown", ev => { if (ev_is(ev)) { ev.preventDefault(); toggle(); } });
  return row;
}
// probeDetails lists what a line's probes saw: whether each block ran, each
// condition's and operand's outcomes, and where values fell against each
// exported constant, with the expression each covers.
function probeDetails(line, source) {
  const row = h("tr", { class: "ut-detail" }), cell = h("td", { colspan: 4 }), list = h("ul");
  const groups = new Map();
  for (const p of line.probes) {
    const key = p.kind === "block" ? "block:" + p.index : "group:" + p.group;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(p);
  }
  const outcome = (p, word) => {
    const reached = !!(p.tests && p.tests.length), span = h("span", { class: "ut-outcome " + (reached ? "full" : "none"), title: reached ? "Reached by " + p.tests.map(id => id.slice(id.indexOf("#") + 1)).join(", ") : "No test reached it" }, `${reached ? "✓" : "✗"} ${word}`);
    return span;
  };
  for (const members of groups.values()) {
    const first = members[0], item = h("li");
    const snippet = first.column ? source.slice(first.column - 1, first.end_column ? first.end_column - 1 : undefined).trim() : "";
    const code = () => { const c = h("code", {}, snippet); item.appendChild(c); };
    if (first.kind === "block") {
      item.appendChild(outcome(first, first.tests && first.tests.length ? "the statements here ran" : "the statements here never ran"));
    } else if (first.kind === "true" || first.kind === "false") {
      item.appendChild(document.createTextNode(first.operand ? "Operand " : "Condition "));
      if (snippet) code();
      item.appendChild(document.createTextNode(": "));
      for (const p of members.sort((a, b) => (a.kind === "true" ? -1 : 1))) { item.appendChild(outcome(p, p.kind)); item.appendChild(document.createTextNode(" ")); }
    } else {
      if (snippet) code();
      item.appendChild(document.createTextNode(` against ${(first.constant || "").split(":").pop()}: `));
      for (const kind of ["below", "at", "above"]) { const p = members.find(m => m.kind === kind); if (p) { item.appendChild(outcome(p, kind)); item.appendChild(document.createTextNode(" ")); } }
      if (first.equality) item.appendChild(h("span", { class: "hint" }, "(== and != need at and one side)"));
    }
    list.appendChild(item);
  }
  cell.appendChild(list);
  row.appendChild(cell);
  return row;
}
