// The AutoPen tab: the plans in .treaty/designs, to read and edit. A save
// goes through what the plan tool does, so text that is not a plan is
// refused with its line and nothing is saved, and a saved plan shows on the
// map and fills in as the code is written.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// autopenState holds the plans as last read (plans), the plan shown, empty
// for a new one (name), its text as loaded or saved (loaded), the last
// problem to show, with its line, and what the last save reported.
const autopenState = { plans: [], name: null, loaded: "", key: "", problem: "", line: 0, busy: false, saved: "" };
const PLAN_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

function autopenEditor() { return document.getElementById("autopen-text"); }
function autopenDirty() { return autopenEditor().value !== autopenState.loaded; }
function planTemplate(name) { return `autopen 1\ndesign "${name || "plan"}"\n\n`; }

// setupAutopen wires the editor once the page knows it is live; a saved map
// has no server to read or write the plans.
function setupAutopen() {
  document.getElementById("autopen-static").hidden = true;
  document.getElementById("autopen-live").hidden = false;
  const editor = autopenEditor(), gutter = document.getElementById("autopen-gutter");
  editor.addEventListener("input", () => { autopenState.problem = ""; autopenState.line = 0; autopenState.saved = ""; paintAutopen(); });
  editor.addEventListener("scroll", () => { gutter.scrollTop = editor.scrollTop; });
  editor.addEventListener("keydown", ev => {
    if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === "s") { ev.preventDefault(); saveAutopen(); }
  });
  document.getElementById("autopen-plan").addEventListener("change", ev => choosePlan(ev.target.value));
  document.getElementById("autopen-name").addEventListener("input", () => { autopenState.problem = ""; paintAutopen(); });
  document.getElementById("autopen-save").addEventListener("click", saveAutopen);
  document.getElementById("autopen-revert").addEventListener("click", () => loadPlan(autopenState.name));
  document.getElementById("autopen-delete").addEventListener("click", deleteAutopen);
  refreshAutopen();
}

// noteAutopenState reads the plans again whenever the map was rebuilt, which
// is when an agent's plan may have changed them.
function noteAutopenState(st) {
  if (document.getElementById("autopen-live").hidden) return;
  const key = String(st.version);
  if (key === autopenState.key) return;
  autopenState.key = key;
  refreshAutopen();
}

async function refreshAutopen() {
  try {
    const response = await fetch("api/plans");
    if (!response.ok) throw new Error((await response.text()).trim());
    autopenState.plans = await response.json();
    const known = autopenState.plans.some(p => p.name === autopenState.name);
    // Keep the person's edits; otherwise show the plan as it now is.
    if (autopenState.name === null || (!known && autopenState.name !== "")) loadPlan(autopenState.plans.length ? autopenState.plans[0].name : "");
    else if (!autopenDirty()) loadPlan(autopenState.name, autopenState.saved);
    else paintAutopen();
  } catch (err) {
    showAutopenProblem("The plans could not be read: " + err.message);
  }
}

// loadPlan shows a plan, or a new one's template when name is empty.
function loadPlan(name, saved) {
  const plan = autopenState.plans.find(p => p.name === name);
  autopenState.name = plan ? name : "";
  autopenState.loaded = plan ? plan.text : planTemplate("");
  autopenState.problem = ""; autopenState.line = 0; autopenState.saved = saved || "";
  autopenEditor().value = autopenState.loaded;
  paintAutopen();
}

function choosePlan(name) {
  if (autopenDirty() && !confirm("Discard your unsaved edits to this plan?")) { paintAutopen(); return; }
  loadPlan(name);
}

async function saveAutopen() {
  if (autopenState.busy) return;
  const name = autopenState.name || document.getElementById("autopen-name").value.trim();
  if (!PLAN_NAME.test(name)) { showAutopenProblem("Name the plan first: letters, digits, dots, dashes and underscores."); return; }
  if (!autopenState.name && autopenState.plans.some(p => p.name === name) && !confirm(`Replace the plan ${name}?`)) return;
  autopenState.busy = true; paintAutopen();
  try {
    const text = autopenEditor().value;
    const response = await fetch("api/plan", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name, text }) });
    if (!response.ok) { showAutopenProblem((await response.text()).trim(), text); return; }
    const report = await response.json(), open = (report.items || []).length;
    autopenState.name = name; autopenState.loaded = text; autopenState.problem = ""; autopenState.line = 0;
    autopenState.saved = `Saved. ${open ? `${open} item${open === 1 ? "" : "s"} to build or fix` : "Everything in it is built"}; the map shows it.`;
    document.getElementById("autopen-name").value = "";
    await refreshAutopen();
  } catch (err) {
    showAutopenProblem("The plan was not saved: " + err.message);
  } finally {
    autopenState.busy = false; paintAutopen();
  }
}

// deleteAutopen deletes the plan shown, once the person confirms it, and
// shows the first plan left, or a new one.
async function deleteAutopen() {
  const name = autopenState.name;
  if (!name || autopenState.busy || !confirm(`Delete the plan ${name}? Its file in .treaty/designs is removed, and the map no longer draws it.`)) return;
  autopenState.busy = true; paintAutopen();
  try {
    const response = await fetch("api/plan/delete", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) });
    if (!response.ok && response.status !== 404) { showAutopenProblem((await response.text()).trim()); return; }
    autopenState.name = null;
    await refreshAutopen();
    autopenState.saved = `Deleted ${name}.`;
  } catch (err) {
    showAutopenProblem("The plan was not deleted: " + err.message);
  } finally {
    autopenState.busy = false; paintAutopen();
  }
}

// showAutopenProblem shows a refusal, and marks the line it names. The
// server puts a header before text without one, so its line numbers then
// run three ahead of the editor's.
function showAutopenProblem(message, text) {
  const line = /\bautopen:(\d+):/.exec(message), headed = text === undefined || /^\s*(autopen|cml) 1\b/.test(text);
  autopenState.problem = message; autopenState.line = line ? Math.max(1, Number(line[1]) - (headed ? 0 : 3)) : 0;
  paintAutopen();
}

function setAutopenStatus(text, problem) {
  const status = document.getElementById("autopen-status");
  status.textContent = text; status.classList.toggle("error", !!problem);
}

// paintAutopen redraws the plan list, the gutter, the buttons and the status
// line.
function paintAutopen() {
  const editor = autopenEditor(), dirty = autopenDirty(), select = document.getElementById("autopen-plan");
  select.replaceChildren(...autopenState.plans.map(p => h("option", { value: p.name }, p.name)), h("option", { value: "" }, "New plan…"));
  select.value = autopenState.name || "";
  document.getElementById("autopen-name").hidden = !!autopenState.name;
  const lines = editor.value.split("\n").length, gutter = document.getElementById("autopen-gutter");
  gutter.replaceChildren();
  for (let i = 1; i <= lines; i++) gutter.appendChild(h("div", i === autopenState.line ? { class: "bad" } : {}, String(i)));
  gutter.scrollTop = editor.scrollTop;
  document.getElementById("autopen-save").disabled = (!dirty && !!autopenState.name) || autopenState.busy;
  document.getElementById("autopen-revert").disabled = !dirty;
  document.getElementById("autopen-delete").disabled = !autopenState.name || autopenState.busy;
  if (autopenState.problem) return setAutopenStatus(autopenState.problem, true);
  if (autopenState.saved && !dirty) return setAutopenStatus(autopenState.saved);
  if (dirty) return setAutopenStatus(`Unsaved changes. ${navigator.platform.startsWith("Mac") ? "⌘" : "Ctrl+"}S saves.`);
  setAutopenStatus(autopenState.name ? "As on disk." : autopenState.plans.length ? "Write a new plan and name it." : "There are no plans yet. Write one here, or ask an agent to plan.");
}
